package httpapi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Reuse the compressor's buffers. Large overview/history responses benefit
// from the standard compression level without changing any financial input.
var listGzipWriters = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// Pro rows carry the full workflow and financial history. Compress transport,
// preserving the API shape and no-store policy rather than dropping details.
func writeProAccountList(w http.ResponseWriter, r *http.Request, data any) {
	body, err := json.Marshal(response{OK: true, Data: data})
	if err != nil {
		writeAPI(w, 500, nil, "读取 Pro 列表失败")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Accept-Encoding")
	if len(body) >= 2048 && acceptsListGzip(r.Header.Get("Accept-Encoding")) {
		var compressed bytes.Buffer
		writer := listGzipWriters.Get().(*gzip.Writer)
		writer.Reset(&compressed)
		_, _ = writer.Write(body)
		_ = writer.Close()
		writer.Reset(nil)
		listGzipWriters.Put(writer)
		if compressed.Len() < len(body) {
			body = compressed.Bytes()
			w.Header().Set("Content-Encoding", "gzip")
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func acceptsListGzip(header string) bool {
	for _, entry := range strings.Split(header, ",") {
		parts := strings.Split(entry, ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
			continue
		}
		for _, option := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(option), "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "q") {
				q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				return err == nil && q > 0 && q <= 1
			}
		}
		return true
	}
	return false
}
