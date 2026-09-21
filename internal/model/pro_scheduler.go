package model

import "time"

type ProScheduleTask struct {
	Email  string            `json:"email"`
	CardID string            `json:"card_id"`
	Status string            `json:"status"`
	Stage  string            `json:"stage"`
	Steps  map[string]string `json:"steps,omitempty"`
	Error  string            `json:"error,omitempty"`
}
type ProScheduleRun struct {
	ID         string            `json:"id"`
	Trigger    string            `json:"trigger"`
	Status     string            `json:"status"`
	Message    string            `json:"message"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
	Tasks      []ProScheduleTask `json:"tasks"`
}
type ProScheduleStatus struct {
	Enabled        bool            `json:"enabled"`
	OpenedUnmerged int             `json:"opened_unmerged"`
	Pending        int             `json:"pending"`
	Maximum        int             `json:"maximum"`
	AvailableCards int             `json:"available_cards"`
	CardCapacity   int             `json:"card_capacity"`
	Needed         int             `json:"needed"`
	NextCheckAt    *time.Time      `json:"next_check_at,omitempty"`
	ServerTime     time.Time       `json:"server_time"`
	Active         *ProScheduleRun `json:"active,omitempty"`
}
