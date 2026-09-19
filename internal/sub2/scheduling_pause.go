package sub2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"chatgpt-space-merge/internal/model"
)

type SchedulingPause struct {
	AccountID          int64     `json:"account_id"`
	Platform           string    `json:"platform"`
	Type               string    `json:"type"`
	Email              string    `json:"email"`
	SchedulingPausedAt time.Time `json:"scheduling_paused_at"`
	PausedSeconds      int64     `json:"paused_seconds"`
}

// A nil result means the account is no longer paused (or no longer exists).
func (c *Client) SchedulingPause(ctx context.Context, settings model.Sub2Settings, password string, id int64) (*SchedulingPause, error) {
	if id < 1 {
		return nil, errors.New("invalid Sub2 account ID")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	query := url.Values{"account_ids": {strconv.FormatInt(id, 10)}}
	data, err := c.doJSON(ctx, settings, password, http.MethodGet, "/api/v1/admin/accounts/scheduling-paused?"+query.Encode(), nil, nil)
	if err != nil {
		return nil, err
	}
	var snapshot struct {
		Version   int                `json:"version"`
		ServerNow time.Time          `json:"server_now"`
		Accounts  *[]SchedulingPause `json:"accounts"`
		HasMore   bool               `json:"has_more"`
	}
	if err = json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Version != 1 || snapshot.ServerNow.IsZero() || snapshot.Accounts == nil || snapshot.HasMore || len(*snapshot.Accounts) > 1 {
		return nil, errors.New("Sub2 paused scheduling response is incomplete or incompatible")
	}
	if len(*snapshot.Accounts) == 0 {
		return nil, nil
	}
	v := (*snapshot.Accounts)[0]
	expectedSeconds := max(0, int64(snapshot.ServerNow.Sub(v.SchedulingPausedAt)/time.Second))
	if v.AccountID != id || v.SchedulingPausedAt.IsZero() || v.PausedSeconds < 0 || v.PausedSeconds != expectedSeconds {
		return nil, errors.New("Sub2 paused scheduling identity or timestamp mismatch")
	}
	return &v, nil
}
