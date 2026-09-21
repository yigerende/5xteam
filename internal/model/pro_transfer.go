package model

import (
	"strings"
	"time"
)

type ProDownstreamRef struct {
	Provider   string `json:"provider"`
	URL        string `json:"url"`
	LoginEmail string `json:"login_email,omitempty"`
}

func ProDownstream(settings ProSettings) ProDownstreamRef {
	if settings.Provider == "cpa" {
		return ProDownstreamRef{Provider: "cpa", URL: strings.TrimRight(strings.TrimSpace(settings.CPA.URL), "/")}
	}
	return ProDownstreamRef{Provider: "sub2", URL: strings.TrimRight(strings.TrimSpace(settings.Sub2.URL), "/"), LoginEmail: strings.ToLower(strings.TrimSpace(settings.Sub2.Email))}
}
func (r ProDownstreamRef) Matches(other ProDownstreamRef) bool {
	return r.Provider == other.Provider && strings.TrimRight(r.URL, "/") != "" && strings.TrimRight(r.URL, "/") == strings.TrimRight(other.URL, "/") && strings.EqualFold(r.LoginEmail, other.LoginEmail)
}

type ProMigrationState struct {
	ImportedAt   time.Time        `json:"imported_at"`
	ExportedAt   time.Time        `json:"exported_at"`
	Fingerprint  string           `json:"fingerprint"`
	Paused       bool             `json:"paused"`
	Downstream   ProDownstreamRef `json:"downstream"`
	TargetTeamID string           `json:"target_team_id,omitempty"`
	Notice       string           `json:"notice,omitempty"`
}
