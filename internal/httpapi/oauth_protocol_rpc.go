package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// One gate coordinates card-pool transactions across all OAuth subprocesses.
// Leases last until the owning child exits, including failures/timeouts.
var oauthSMSGate = make(chan struct{}, 1)
var oauthSMSLeases = map[string]*oauthProtocolRPC{}

type oauthProtocolRPC struct {
	server *Server
	ctx    context.Context
	email  string
	locked bool
	owned  map[string]bool
}

func (p *oauthProtocolRPC) lock() error {
	if p.locked {
		return nil
	}
	select {
	case oauthSMSGate <- struct{}{}:
		p.locked = true
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}

func (p *oauthProtocolRPC) unlock() {
	if p.locked {
		p.locked = false
		<-oauthSMSGate
	}
}

func (p *oauthProtocolRPC) close() {
	if len(p.owned) == 0 {
		p.unlock()
		return
	}
	// Cleanup must run even after this job's context has expired.
	if !p.locked {
		oauthSMSGate <- struct{}{}
		p.locked = true
	}
	for id, owner := range oauthSMSLeases {
		if owner == p {
			delete(oauthSMSLeases, id)
		}
	}
	p.unlock()
}

func (p *oauthProtocolRPC) call(method string, args map[string]any) (any, error) {
	str := func(key string) string { value, _ := args[key].(string); return value }
	switch method {
	case "sms_lock":
		return true, p.lock()
	case "sms_unlock":
		p.unlock()
		return true, nil
	case "sms_config":
		return p.server.store.SMSConfigRaw(str("provider"))
	case "sms_save_config":
		if !p.locked {
			return nil, errors.New("SMS config mutation requires the local card-pool lock")
		}
		cfg, _ := args["config"].(map[string]any)
		enabled, _ := args["enabled"].(bool)
		return true, p.server.store.SaveSMSConfig(str("provider"), cfg, enabled)
	case "sms_pick":
		if err := p.lock(); err != nil {
			return nil, err
		}
		defer p.unlock()
		excluded := make(map[string]bool)
		for id := range oauthSMSLeases {
			excluded[id] = true
		}
		if ids, ok := args["exclude_ids"].([]any); ok {
			for _, id := range ids {
				if value, ok := id.(string); ok {
					excluded[value] = true
				}
			}
		}
		phone, err := p.server.store.OAuthSMSPhone(p.email, str("provider"), excluded)
		if err == nil && phone != nil {
			id := phone["id"].(string)
			oauthSMSLeases[id] = p
			if p.owned == nil {
				p.owned = make(map[string]bool)
			}
			p.owned[id] = true
		}
		return phone, err
	case "sms_rejected", "sms_bind":
		if err := p.lock(); err != nil {
			return nil, err
		}
		defer p.unlock()
		id := str("id")
		if oauthSMSLeases[id] != p {
			return nil, errors.New("OAuth job does not own this SMS phone")
		}
		if method == "sms_rejected" {
			disable, _ := args["disable"].(bool)
			return true, p.server.store.RejectOAuthSMSPhone(id, str("reason"), disable)
		}
		if p.server.store.OAuthSMSBound(id, p.email) {
			return true, nil
		}
		return true, p.server.store.BindSMSPhone(id, p.email)
	default:
		return nil, fmt.Errorf("unsupported local OAuth integration: %s", method)
	}
}

// runOAuthChild uses newline-delimited JSON for RPC and the final result.
// Credentials and SMS config never appear in process arguments or diagnostics.
func runOAuthChild(ctx context.Context, cmd *exec.Cmd, payload map[string]any,
	rpc func(string, map[string]any) (any, error), stderrLine func(string)) (map[string]any, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			if stderrLine != nil {
				stderrLine(scanner.Text())
			}
		}
		done <- scanner.Err()
	}()
	encoder := json.NewEncoder(stdin)
	if err = encoder.Encode(payload); err != nil {
		_ = cmd.Process.Kill()
	}
	var result map[string]any
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for err == nil && scanner.Scan() {
		var message map[string]any
		if err = json.Unmarshal(scanner.Bytes(), &message); err != nil {
			break
		}
		if method, ok := message["rpc"].(string); ok {
			params, _ := message["params"].(map[string]any)
			value, rpcErr := rpc(method, params)
			reply := map[string]any{"result": value}
			if rpcErr != nil {
				reply["error"] = rpcErr.Error()
			}
			err = encoder.Encode(reply)
		} else if _, ok := message["success"].(bool); ok {
			result = message
		} else {
			err = errors.New("invalid OAuth child message")
		}
	}
	if err == nil {
		err = scanner.Err()
	}
	if err != nil {
		_ = cmd.Process.Kill()
	}
	_ = stdin.Close()
	stderrErr := <-done
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if waitErr != nil {
		return nil, waitErr
	}
	if stderrErr != nil {
		return nil, stderrErr
	}
	if result == nil {
		return nil, errors.New("OAuth child returned no result")
	}
	return result, nil
}
