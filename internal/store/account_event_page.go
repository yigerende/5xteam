package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"chatgpt-space-merge/internal/model"
)

var ErrInvalidEventCursor = errors.New("invalid account event cursor")

type accountEventCursor struct {
	AccountID string `json:"account_id"`
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

type AccountEventPage struct {
	Events     []model.AutoRotationEvent `json:"events"`
	HasMore    bool                      `json:"has_more"`
	NextCursor string                    `json:"next_cursor"`
}

func (s *Store) AccountEventsPage(accountID, cursor string, limit int) (AccountEventPage, error) {
	page := AccountEventPage{Events: []model.AutoRotationEvent{}}
	if limit < 1 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	query := "SELECT payload, created_at, id FROM auto_rotation_events WHERE account_id=?"
	args := []any{accountID}
	if cursor != "" {
		if len(cursor) > 2048 {
			return page, ErrInvalidEventCursor
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		var after accountEventCursor
		if err != nil || json.Unmarshal(raw, &after) != nil || after.AccountID != accountID || after.ID == "" {
			return page, ErrInvalidEventCursor
		}
		if _, err := time.Parse(time.RFC3339Nano, after.CreatedAt); err != nil {
			return page, ErrInvalidEventCursor
		}
		query += " AND (created_at, id) < (?, ?)"
		args = append(args, after.CreatedAt, after.ID)
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit+1)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var last accountEventCursor
	for rows.Next() {
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		var raw, createdAt, id string
		var event model.AutoRotationEvent
		if err := rows.Scan(&raw, &createdAt, &id); err != nil {
			return page, err
		}
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return page, err
		}
		page.Events = append(page.Events, event)
		last = accountEventCursor{AccountID: accountID, CreatedAt: createdAt, ID: id}
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if page.HasMore {
		raw, err := json.Marshal(last)
		if err != nil {
			return page, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}
