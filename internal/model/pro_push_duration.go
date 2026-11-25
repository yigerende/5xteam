package model

import "time"

// Start once: successful activation owns the clock, later pushes never reset it.
// Accounts imported without activation evidence retain the first-push fallback.
func (p *MailAccountProfile) StartProPushClock(at time.Time, source string) bool {
	if at.IsZero() || (p.ProPushStartedAt != nil && !p.ProPushStartedAt.IsZero()) {
		return false
	}
	p.ProPushStartedAt = &at
	p.ProPushStartedSource = source
	return true
}

// A display clock only: this does not change quota, timeout or retry policies.
func (p MailAccountProfile) ProPushEndTime() *time.Time {
	if p.ProPushEndedAt != nil && !p.ProPushEndedAt.IsZero() {
		return p.ProPushEndedAt
	}
	var end *time.Time
	add := func(at time.Time) {
		if at.IsZero() || (p.ProPushStartedAt != nil && at.Before(*p.ProPushStartedAt)) {
			return
		}
		if end == nil || at.Before(*end) {
			value := at
			end = &value
		}
	}
	for _, stage := range []string{"merge", "deactivate", "merchant", "merge_v2"} {
		add(p.ProManualStages[stage].StartedAt)
		for _, state := range []string{"running", "completed", "failed"} {
			add(p.ProAuto.StageTimes[stage+":"+state])
		}
	}
	if p.ProDeactivation != nil {
		add(p.ProDeactivation.StartedAt)
		if p.ProDeactivation.AcceptedAt != nil {
			add(*p.ProDeactivation.AcceptedAt)
		}
		if p.ProDeactivation.ConfirmedAt != nil {
			add(*p.ProDeactivation.ConfirmedAt)
		}
	}
	if p.ProMergeV2 != nil {
		add(p.ProMergeV2.StartedAt)
	}
	if p.ProMerchant != nil {
		add(p.ProMerchant.StartedAt)
	}
	if p.SpaceMergedAt != nil {
		add(*p.SpaceMergedAt)
	}
	return end
}

func (p *MailAccountProfile) FreezeProPushDuration(now time.Time) {
	if p.ProPushStartedAt == nil || p.ProPushStartedAt.IsZero() {
		return
	}
	if end := p.ProPushEndTime(); end != nil {
		p.ProPushEndedAt = end
		return
	}
	if p.ProWorkflowRunning || p.SpaceMergedOnce || p.DispositionStarted() ||
		(p.ProAuto.Status == "running" && (p.ProAuto.Stage == "merge" || p.ProAuto.Stage == "deactivate")) {
		p.ProPushEndedAt = &now
	}
}
