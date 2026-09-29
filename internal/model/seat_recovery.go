package model

import "time"

// Seat recovery deliberately has no relationship to FreeAccount cycles.
type SeatRecoverySettings struct {
	JoinMethod               string `json:"join_method"`
	RemoveMethod             string `json:"remove_method"`
	DwellMinutes             int    `json:"dwell_minutes"`
	OperationIntervalSeconds int    `json:"operation_interval_seconds"`
	NextIntervalSeconds      int    `json:"next_interval_seconds"`
	Concurrency              int    `json:"concurrency"`
}

func DefaultSeatRecoverySettings() SeatRecoverySettings {
	return SeatRecoverySettings{JoinMethod: "mother_invite", RemoveMethod: "mother_kick", DwellMinutes: 5, OperationIntervalSeconds: 10, NextIntervalSeconds: 10, Concurrency: 2}
}

type SeatRecoveryTask struct {
	ID            string               `json:"id"`
	AdminID       string               `json:"admin_id"`
	AdminLabel    string               `json:"admin_label"`
	TeamID        string               `json:"team_id"`
	Email         string               `json:"email"`
	UserID        string               `json:"user_id"`
	Settings      SeatRecoverySettings `json:"settings"`
	Stage         string               `json:"stage"`
	Status        string               `json:"status"`
	Message       string               `json:"message"`
	Paused        bool                 `json:"paused"`
	Finished      bool                 `json:"finished"`
	Lane          bool                 `json:"lane"`
	SeatType      string               `json:"seat_type"`
	FreeID        string               `json:"free_id,omitempty"`
	FreeCycle     string               `json:"free_cycle,omitempty"`
	JoinSent      bool                 `json:"join_sent"`
	ConfirmSent   bool                 `json:"confirm_sent"`
	SwitchSent    bool                 `json:"switch_sent"`
	LeaveSent     bool                 `json:"leave_sent"`
	RetryMutation bool                 `json:"retry_mutation"`
	FlowID        string               `json:"flow_id"`
	MutationID    string               `json:"mutation_id"`
	Checks        int                  `json:"checks"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
	JoinedAt      *time.Time           `json:"joined_at,omitempty"`
	DueAt         *time.Time           `json:"due_at,omitempty"`
	LeftAt        *time.Time           `json:"left_at,omitempty"`
	NextAt        time.Time            `json:"next_at"`
	Capacity      *AdminSeatCapacity   `json:"capacity,omitempty"`
}

type SeatRecoveryLog struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	Stage   string    `json:"stage"`
	Message string    `json:"message"`
}
