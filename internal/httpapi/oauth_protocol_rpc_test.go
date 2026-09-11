package httpapi

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"testing"
	"time"

	"chatgpt-space-merge/internal/store"
	"chatgpt-space-merge/internal/workflow"
)

func TestOAuthStructuredRetryDecisions(t *testing.T) {
	for _, tc := range []struct {
		result map[string]any
		want   bool
	}{
		{map[string]any{"error_code": "oauth_session_missing", "error": "HTTP 403", "retryable": true}, true},
		{map[string]any{"error_code": "passkey_or_challenge", "error": "timeout", "retryable": false}, false},
		{map[string]any{"dead": true, "error": "HTTP 429", "retryable": true}, false},
		{map[string]any{"error_code": "phone_2fa_rate_limited", "retryable": true}, true},
		{map[string]any{"error": "network error: timeout", "retryable": true}, true},
		{map[string]any{"error": "invalid_auth_step", "retryable": false}, false},
	} {
		if got := isRetryableOAuthNetworkResult(tc.result, nil); got != tc.want {
			t.Fatalf("%v: got %v", tc.result, got)
		}
	}
}

func TestOAuthSMSConcurrentLeasesAndBoundReuse(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &Server{store: db}
	const workers = 8
	for i := 0; i < workers; i++ {
		if _, err = db.AddSMSPhone("generic", fmt.Sprintf("+155500000%02d", i), "", "https://sms.example", "", "", 1); err != nil {
			t.Fatal(err)
		}
	}
	jobs := make([]*oauthProtocolRPC, workers)
	ids := make(chan string, workers)
	var wg sync.WaitGroup
	for i := range jobs {
		jobs[i] = &oauthProtocolRPC{server: server, ctx: context.Background(), email: fmt.Sprintf("user%d@example.com", i)}
		wg.Add(1)
		go func(p *oauthProtocolRPC) {
			defer wg.Done()
			row, e := p.call("sms_pick", map[string]any{"provider": "generic"})
			if e != nil || row == nil {
				t.Errorf("pick: %v / %v", row, e)
				return
			}
			id := row.(map[string]any)["id"].(string)
			ids <- id
			if _, e = p.call("sms_bind", map[string]any{"id": id}); e != nil {
				t.Error(e)
			}
			if _, e = p.call("sms_bind", map[string]any{"id": id}); e != nil {
				t.Errorf("idempotent bind: %v", e)
			}
		}(jobs[i])
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate active phone: %s", id)
		}
		seen[id] = true
	}
	if len(seen) != workers {
		t.Fatalf("got %d phones", len(seen))
	}
	for _, p := range jobs {
		p.close()
	}
	reuse := &oauthProtocolRPC{server: server, ctx: context.Background(), email: "user0@example.com"}
	defer reuse.close()
	row, err := reuse.call("sms_pick", map[string]any{"provider": "generic"})
	if err != nil || row == nil {
		t.Fatalf("bound phone cannot be reused: %v %v", row, err)
	}
	if row.(map[string]any)["own_binding"] != true {
		t.Fatal("did not reuse own bound phone")
	}
}

func TestOAuthSMSLockCancellation(t *testing.T) {
	p := &oauthProtocolRPC{ctx: context.Background()}
	if err := p.lock(); err != nil {
		t.Fatal(err)
	}
	defer p.close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	other := &oauthProtocolRPC{ctx: ctx}
	defer other.close()
	if err := other.lock(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait: %v", err)
	}
}

func TestOAuthChildStdioRPCAndErrors(t *testing.T) {
	python := workflow.FindPython()
	if python == "" {
		t.Skip("Python is checked in the runtime image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	script := `import json,sys
p=json.loads(sys.stdin.readline())
print(json.dumps({'rpc':'sms_config','params':{'provider':'hero_sms'}}),flush=True)
r=json.loads(sys.stdin.readline())
print('[protocol] fixture step',file=sys.stderr,flush=True)
print(json.dumps({'success':r['result']['enabled'],'email':p['email']}),flush=True)
`
	var lines int
	result, err := runOAuthChild(ctx, exec.CommandContext(ctx, python, "-c", script), map[string]any{"email": "fixture@example.com"}, func(method string, args map[string]any) (any, error) {
		if method != "sms_config" || args["provider"] != "hero_sms" {
			t.Error("wrong RPC")
		}
		return map[string]any{"enabled": true}, nil
	}, func(string) { lines++ })
	if err != nil || result["success"] != true || result["email"] != "fixture@example.com" || lines != 1 {
		t.Fatalf("result=%v err=%v lines=%d", result, err, lines)
	}
	_, err = runOAuthChild(ctx, exec.CommandContext(ctx, python, "-c", "print('invalid json')"), map[string]any{}, nil, nil)
	if err == nil {
		t.Fatal("invalid child output accepted")
	}
}

func TestOAuthChildCancellation(t *testing.T) {
	python := workflow.FindPython()
	if python == "" {
		t.Skip("Python is checked in the runtime image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := runOAuthChild(ctx, exec.CommandContext(ctx, python, "-c", "import time; time.sleep(30)"), map[string]any{}, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
}
