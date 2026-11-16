package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"chatgpt-space-merge/internal/store"
)

type ejhFinanceRecording struct {
	StartedAt   time.Time  `json:"started_at"`
	LastSuccess *time.Time `json:"last_success,omitempty"`
	Error       string     `json:"error,omitempty"`
}

func (s *Server) captureEJHFinance() {
	var state ejhFinanceRecording
	if err := s.store.EJHGet("finance_recording", "latest", &state); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return
	}
	now := time.Now()
	if state.StartedAt.IsZero() {
		state.StartedAt = now
	}
	if err := s.store.CaptureEJHFinance(now); err != nil {
		state.Error = err.Error()
	} else {
		state.LastSuccess = &now
		state.Error = ""
	}
	_ = s.store.EJHPut("finance_recording", "latest", state)
}

func (s *Server) ejhFinance(w http.ResponseWriter, r *http.Request) {
	reader, err := s.store.ProListReader(r.Context())
	if err != nil {
		writeAPI(w, 500, nil, "读取成本历史失败")
		return
	}
	defer reader.Close()
	(&Server{store: reader}).ejhFinanceSnapshot(w, r)
}

func (s *Server) ejhFinanceSnapshot(w http.ResponseWriter, r *http.Request) {
	transfers, err := store.EJHRecords[store.EJHExternalTransferEvent](s.store, "finance_external_transfer")
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	recharges, err := store.EJHRecords[store.EJHRechargeEvent](s.store, "finance_recharge")
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	usage, err := store.EJHRecords[store.EJHConsumption](s.store, "finance_usage")
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	days, err := store.EJHRecords[store.EJHConsumptionDay](s.store, "finance_usage_day")
	if err != nil {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	// The cost page needs dated ledger entries, not copies of every historical
	// card/trade snapshot. Do not even decrypt those large records in compact mode.
	snapshots := []store.EJHFinancialSnapshot{}
	if r.URL.Query().Get("snapshots") != "none" {
		snapshots, err = store.EJHRecords[store.EJHFinancialSnapshot](s.store, "finance_snapshot")
		if err != nil {
			writeAPI(w, 500, nil, err.Error())
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	var state ejhFinanceRecording
	if err = s.store.EJHGet("finance_recording", "latest", &state); err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeAPI(w, 500, nil, err.Error())
		return
	}
	writeProAccountList(w, r, map[string]any{"external_transfers": transfers, "recharges": recharges, "consumption": usage, "usage_days": days, "snapshots": snapshots, "recording": state, "server_time": time.Now()})
}
