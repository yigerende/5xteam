package httpapi

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgpt-space-merge/internal/model"
	"chatgpt-space-merge/internal/store"
)

type exportTestEvent struct {
	Type      string            `json:"type"`
	Stage     string            `json:"stage"`
	Processed int               `json:"processed"`
	Total     int               `json:"total"`
	Size      int               `json:"size"`
	Error     string            `json:"error"`
	Headers   map[string]string `json:"headers"`
}

func parseExportTestStream(t *testing.T, raw []byte) ([]exportTestEvent, []byte) {
	t.Helper()
	reader := bufio.NewReader(bytes.NewReader(raw))
	var events []exportTestEvent
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			return events, nil
		}
		if err != nil {
			t.Fatal(err)
		}
		var event exportTestEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
		if event.Type == "file" {
			file, err := io.ReadAll(reader)
			if err != nil || len(file) != event.Size {
				t.Fatal("incorrect download length", err)
			}
			return events, file
		}
	}
}

func TestMailExportProgressAllFormats(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	emails := []string{"first@example.com", "second@example.com"}
	for _, email := range emails {
		_, err := st.SaveMailAccount(model.MailAccountProfile{Email: email}, model.MailAccountCredentials{Email: email, GptPassword: "fixture-password", TotpSecret: "fixture-totp", AccessToken: turbIntegrationTestToken(email), RefreshToken: "fixture-rt"})
		if err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{store: st}
	for _, format := range []string{"text", "cpa", "sub2"} {
		t.Run(format, func(t *testing.T) {
			payload, _ := json.Marshal(batchMailCredentialExportInput{Emails: emails, Format: format, IncludeRT: true})
			w := httptest.NewRecorder()
			s.exportMailAccountCredentialsProgress(w, httptest.NewRequest("POST", "/export", bytes.NewReader(payload)))
			if w.Code != 200 || !w.Flushed || w.Header().Get("X-Accel-Buffering") != "no" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("export is not streamed safely", w.Header())
			}
			events, file := parseExportTestStream(t, w.Body.Bytes())
			last := 0
			generated := false
			for _, event := range events {
				if event.Type != "progress" {
					continue
				}
				if event.Total != 2 || event.Processed < last || event.Processed > 2 {
					t.Fatal("non-monotonic/incorrect progress", events)
				}
				last = event.Processed
				generated = generated || event.Stage == "generating"
			}
			if last != 2 || !generated || events[0].Processed != 0 || events[len(events)-1].Type != "file" {
				t.Fatal("missing progress stages", events)
			}
			metadata, _ := json.Marshal(events)
			if bytes.Contains(metadata, []byte("fixture-password")) || bytes.Contains(metadata, []byte("fixture-rt")) {
				t.Fatal("credential in progress metadata")
			}
			switch format {
			case "text":
				if strings.Count(string(file), "----fixture-password----fixture-totp----fixture-rt\r\n") != 2 {
					t.Fatal("text payload mismatch")
				}
			case "cpa":
				archive, err := zip.NewReader(bytes.NewReader(file), int64(len(file)))
				if err != nil || len(archive.File) != 2 {
					t.Fatal("corrupted ZIP", err)
				}
			case "sub2":
				var out struct{ Accounts []json.RawMessage }
				if err := json.Unmarshal(file, &out); err != nil || len(out.Accounts) != 2 {
					t.Fatal("corrupted Sub2 JSON", err)
				}
			}
		})
	}
	for _, format := range []string{"text", "cpa", "sub2", "bad"} {
		payload, _ := json.Marshal(batchMailCredentialExportInput{Emails: append(emails, "missing@example.com"), Format: format})
		w := httptest.NewRecorder()
		s.exportMailAccountCredentialsProgress(w, httptest.NewRequest("POST", "/export", bytes.NewReader(payload)))
		events, file := parseExportTestStream(t, w.Body.Bytes())
		if len(file) != 0 || len(events) == 0 || events[len(events)-1].Type != "error" || events[len(events)-1].Error == "" {
			t.Fatal("failure produced partial download", events)
		}
		if strings.Contains(w.Body.String(), "fixture-password") || strings.Contains(w.Body.String(), "fixture-rt") {
			t.Fatal("failed export leaked credentials")
		}
	}
}

func TestMailExportProgressCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/export", strings.NewReader(`{"emails":["a@example.com"],"format":"text"}`)).WithContext(ctx)
	// A cancelled export must not even read the database.
	(&Server{}).exportMailAccountCredentialsProgress(w, r)
	if w.Body.Len() != 0 {
		t.Fatal("cancelled export returned a file")
	}
}
