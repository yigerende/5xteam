package store

import (
	"chatgpt-space-merge/internal/model"
	"sync"
	"sync/atomic"
	"testing"
)

func TestImportMailAccountAtomicCreatedResult(t *testing.T) {
	s := transferStore(t)
	var creates atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, e := s.ImportMailAccount(model.MailAccountProfile{Email: " SAME@example.com "}, model.MailAccountCredentials{Email: "same@example.com"}, true)
			if e != nil {
				t.Error(e)
			}
			if created {
				creates.Add(1)
			}
		}()
	}
	wg.Wait()
	if creates.Load() != 1 {
		t.Fatal("duplicate imports counted as new", creates.Load())
	}
	p, _, e := s.MailAccountCredential("same@example.com")
	if e != nil || p.ManagementScope != "pro" || p.ProManagedAt == nil {
		t.Fatal(p, e)
	}
	again, created, e := s.ImportMailAccount(model.MailAccountProfile{Email: "same@example.com"}, model.MailAccountCredentials{Email: "same@example.com", GptPassword: "updated"}, false)
	if e != nil || created || again.ID != p.ID || !again.CreatedAt.Equal(p.CreatedAt) || again.ManagementScope != "pro" {
		t.Fatal("reimport reset identity/ownership", e)
	}
}
