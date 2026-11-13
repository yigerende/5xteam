package model

import "time"

// Keep this order aligned with accountDisposition's unfiltered row selection.
// Completion uses the same projection (including manual annotations) as the UI.
func (p MailAccountProfile) FinalProStage(fallback string) string {
	valid := func(s string) bool { return s == "merge" || s == "merge_v2" || s == "merchant" || s == "deactivate" }
	if valid(p.ProAuto.Stage) {
		return p.ProAuto.Stage
	}
	if p.ProMergeV2 != nil {
		return "merge_v2"
	}
	if p.MerchantStarted() {
		return "merchant"
	}
	if p.ProDeactivation != nil {
		return "deactivate"
	}
	if p.SpaceMergedOnce || p.TransferStatus == "running" || p.TransferStatus == "completed" || p.TransferStatus == "unknown" || p.TransferStatus == "failed" {
		return "merge"
	}
	steps := p.ProStages().Steps
	for _, stage := range []string{"merge", "deactivate", "merchant", "merge_v2"} {
		if state := steps[stage]; state != "" && state != "pending" && state != "not_started" {
			return stage
		}
	}
	if valid(fallback) {
		return fallback
	}
	return "merge"
}

type SIWCWorkflowMarker struct {
	SourceID  string    `json:"source_id"`
	Revision  int64     `json:"revision"`
	Email     string    `json:"email"`
	FlowID    string    `json:"flow_id"`
	OrderID   string    `json:"order_id,omitempty"`
	Stage     string    `json:"stage"`
	Completed bool      `json:"completed"`
	Manual    bool      `json:"manual"`
	ChangedAt time.Time `json:"changed_at"`
}

func (p MailAccountProfile) SIWCWorkflowMarker(source string, revision int64, changed time.Time, fallback string) SIWCWorkflowMarker {
	stage := p.FinalProStage(fallback)
	display := p.ProStages()
	// The list's final projection applies explicit manual annotations last.
	p.ApplyProStageOverrides(&display)
	flow := p.ProAuto.ID
	if flow == "" {
		flow = p.ID
	}
	if p.ProMergeV2Restart != nil {
		flow += ":" + p.ProMergeV2Restart.ID
	}
	return SIWCWorkflowMarker{SourceID: source, Revision: revision, Email: p.Email, FlowID: flow, OrderID: p.ProAuto.OrderID, Stage: stage, Completed: display.Steps[stage] == "completed", Manual: display.Manual[stage], ChangedAt: changed}
}
