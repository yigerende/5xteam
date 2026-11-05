package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
)

// Display transport only. Every revision contains the same complete financial
// inputs as the legacy overview; never use these snapshots for admission.
type ejhOverviewSnapshot struct {
	revision       string
	sourceRevision string
	fields         map[string]json.RawMessage
}
type ejhOverviewHistory struct {
	mu        sync.Mutex
	snapshots []*ejhOverviewSnapshot
}

func (h *ejhOverviewHistory) response(data map[string]any, sourceRevision, since string) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var current *ejhOverviewSnapshot
	for _, snapshot := range h.snapshots {
		if snapshot.sourceRevision == sourceRevision {
			current = snapshot
			break
		}
	}
	if current == nil {
		fields := make(map[string]json.RawMessage, len(data))
		for key, value := range data {
			if key == "revision" || key == "server_time" {
				continue
			}
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			fields[key] = raw
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		current = &ejhOverviewSnapshot{revision: hex.EncodeToString(sum[:]), sourceRevision: sourceRevision, fields: fields}
		// Retain only three immutable versions; unknown/evicted bases get a full
		// response. Multiple browsers cannot create unbounded per-client state.
		kept := []*ejhOverviewSnapshot{current}
		for _, old := range h.snapshots {
			if old.revision != current.revision && len(kept) < 3 {
				kept = append(kept, old)
			}
		}
		h.snapshots = kept
	}
	out := map[string]any{"revision": current.revision, "source_revision": sourceRevision, "server_time": data["server_time"]}
	if since == current.revision {
		out["mode"] = "unchanged"
		return out, nil
	}
	var previous *ejhOverviewSnapshot
	for _, snapshot := range h.snapshots {
		if snapshot.revision == since {
			previous = snapshot
			break
		}
	}
	if previous == nil {
		out["mode"], out["data"] = "full", current.fields
		return out, nil
	}
	changed := map[string]any{}
	removed := []string{}
	for key := range previous.fields {
		if _, ok := current.fields[key]; !ok {
			removed = append(removed, key)
		}
	}
	for key, raw := range current.fields {
		old := previous.fields[key]
		if bytes.Equal(raw, old) {
			continue
		}
		if patch, ok := ejhOverviewArrayPatch(old, raw); ok {
			changed[key] = map[string]any{"rows": patch}
		} else {
			changed[key] = map[string]any{"value": raw}
		}
	}
	out["mode"], out["base_revision"], out["changes"], out["removed"] = "delta", since, changed, removed
	return out, nil
}

func ejhOverviewRows(raw json.RawMessage) ([]string, map[string]json.RawMessage, bool) {
	var rows []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &rows) != nil {
		return nil, nil, false
	}
	order := []string{}
	byID := map[string]json.RawMessage{}
	for _, row := range rows {
		var identity struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(row, &identity) != nil || identity.ID == "" {
			return nil, nil, false
		}
		if _, exists := byID[identity.ID]; exists {
			return nil, nil, false
		}
		order = append(order, identity.ID)
		byID[identity.ID] = row
	}
	return order, byID, true
}

func ejhOverviewArrayPatch(before, after json.RawMessage) (any, bool) {
	_, old, ok := ejhOverviewRows(before)
	if !ok {
		return nil, false
	}
	order, next, ok := ejhOverviewRows(after)
	if !ok {
		return nil, false
	}
	upsert := []json.RawMessage{}
	removed := []string{}
	for id := range old {
		if _, exists := next[id]; !exists {
			removed = append(removed, id)
		}
	}
	for _, id := range order {
		if !bytes.Equal(old[id], next[id]) {
			upsert = append(upsert, next[id])
		}
	}
	patch := map[string]any{"order": order, "upsert": upsert, "removed": removed}
	encoded, err := json.Marshal(patch)
	return patch, err == nil && len(encoded) < len(after)
}
