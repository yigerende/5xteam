package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chatgpt-space-merge/internal/gptpay"
	"chatgpt-space-merge/internal/restock"
)

// Only replenishment admission uses this predicate. Payment/card occupancy and
// cost reservations continue using the original status and original order.
const restockReplacementDeliveredSQL = `(coalesce(json_extract(payload,'$.fulfilled_by_task_id'),'')<>'' AND coalesce(json_extract(payload,'$.fulfilled_by_email'),'')<>'' AND coalesce(julianday(json_extract(payload,'$.fulfilled_at'))>julianday('0001-01-01T00:00:00Z'),0))`

// Reconcile only currently unresolved tasks, following indexed, explicit chain
// links. No external calls, full mailbox scan or credential decryption occurs.
func (s *Store) ReconcileProRestockDeliveries(tasks []restock.Task) error {
	candidates := []int{}
	for i, t := range tasks {
		if !t.ReplacementDelivered() && t.StoppedAt == nil && !t.ManagedExternally && t.DecisionID != "" && (t.Status == "unknown" || t.Status == "attention") {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, i := range candidates {
		t := &tasks[i]
		var raw, projected string
		if err = tx.QueryRow("SELECT payload,"+proRestockTaskProjectionSQL+" FROM pro_restock_tasks WHERE id=?", t.ID).Scan(&raw, &projected); err != nil {
			return err
		}
		if raw != t.ObservedPayload {
			var current restock.Task
			if err = json.Unmarshal([]byte(projected), &current); err != nil {
				return err
			}
			current.ObservedPayload = raw
			*t = current
			continue
		}
		leaf, found, err := replacementDelivery(tx, *t)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		t.FulfilledByTaskID, t.FulfilledByEmail, t.FulfilledAt = leaf.ID, leaf.Email, leaf.ReadyAt
		t.ETA, t.UpdatedAt = nil, time.Now()
		// Keep Status, OrderID, CardID, Cost and all original order/account
		// facts intact. Only the separate delivery milestone releases inflight.
		next := mustJSON(*t)
		result, err := tx.Exec(`UPDATE pro_restock_tasks SET payload=? WHERE id=? AND payload=?`, next, t.ID, raw)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return err
			}
			return errors.New("补货换号结果已变化，等待重新核验")
		}
		t.ObservedPayload = next
	}
	return tx.Commit()
}

func replacementDelivery(tx *sql.Tx, source restock.Task) (restock.Task, bool, error) {
	current := source
	visited := map[string]bool{}
	root := ""
	for range 64 {
		if visited[current.ID] {
			return restock.Task{}, false, nil
		}
		visited[current.ID] = true
		p, err := replacementProfile(tx, current.Email)
		missing := errors.Is(err, sql.ErrNoRows)
		if err != nil && !missing {
			return restock.Task{}, false, err
		}
		nextID, nextEmail := "", ""
		if missing {
			// Account deletion must not erase a persisted replacement relation.
			// For legacy tasks, the exact original payment embeds its flow ID;
			// still require the child task and account's explicit backlink below.
			var raw string
			err = tx.QueryRow(`SELECT profile FROM gptpay_orders WHERE id=? AND email=?`, current.OrderID, current.Email).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				return restock.Task{}, false, nil
			}
			if err != nil {
				return restock.Task{}, false, err
			}
			var order gptpay.Order
			if err = json.Unmarshal([]byte(raw), &order); err != nil {
				return restock.Task{}, false, err
			}
			if order.Email != current.Email || order.ID != current.OrderID || !strings.HasPrefix(order.ID, "pro-auto-") || len(order.ID) <= len("pro-auto-") {
				return restock.Task{}, false, nil
			}
			nextID = "recharge:" + strings.TrimPrefix(order.ID, "pro-auto-")
		} else {
			r := p.ProAuto.Replacement
			if p.ProRestockTaskID != current.ID || p.ProAuto.ID == "" || r == nil || r.NextEmail == "" || r.RootEmail == "" {
				return restock.Task{}, false, nil
			}
			if root == "" {
				root = r.RootEmail
			}
			if r.RootEmail != root {
				return restock.Task{}, false, nil
			}
			nextID, nextEmail = "recharge:"+p.ProAuto.ID, r.NextEmail
		}
		var raw string
		err = tx.QueryRow(`SELECT payload FROM pro_restock_tasks WHERE id=?`, nextID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return restock.Task{}, false, nil
		}
		if err != nil {
			return restock.Task{}, false, err
		}
		var next restock.Task
		if err = json.Unmarshal([]byte(raw), &next); err != nil {
			return restock.Task{}, false, err
		}
		if next.ID != nextID || (nextEmail != "" && next.Email != nextEmail) || next.DecisionID != source.DecisionID || next.CreatedAt.Before(current.CreatedAt) || next.StoppedAt != nil || next.Status == "cancelled" {
			return restock.Task{}, false, nil
		}
		np, err := replacementProfile(tx, next.Email)
		if errors.Is(err, sql.ErrNoRows) {
			return restock.Task{}, false, nil
		}
		if err != nil {
			return restock.Task{}, false, err
		}
		nr := np.ProAuto.Replacement
		if nr != nil && root == "" {
			root = nr.RootEmail
		}
		if np.ProRestockTaskID != next.ID || nr == nil || nr.PreviousEmail != current.Email || nr.RootEmail == "" || nr.RootEmail != root {
			return restock.Task{}, false, nil
		}
		if next.Status == "ready" && next.ReadyAt != nil && !next.ReadyAt.Before(next.CreatedAt) && next.AccountID > 0 && next.OrderID != "" && next.OrderID == np.ProAuto.OrderID {
			err = tx.QueryRow(`SELECT profile FROM gptpay_orders WHERE id=? AND email=?`, next.OrderID, next.Email).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				return restock.Task{}, false, nil
			}
			if err != nil {
				return restock.Task{}, false, err
			}
			var order gptpay.Order
			if err = json.Unmarshal([]byte(raw), &order); err != nil {
				return restock.Task{}, false, err
			}
			return next, order.Status == "success" && order.Email == next.Email, nil
		}
		current = next
	}
	return restock.Task{}, false, nil
}
