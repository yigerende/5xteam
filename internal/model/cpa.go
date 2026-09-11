package model

// CPASettings configures the local CPA management API used as an alternative
// downstream provider. The key is encrypted by Store and never returned.
type CPASettings struct {
	URL                            string   `json:"url"`
	KeyPresent                     bool     `json:"key_present"`
	Websockets                     bool     `json:"websockets"`
	Enable401Check                 bool     `json:"enable_401_check"`
	StatusCheckIntervalSeconds     int      `json:"status_check_interval_seconds"`
	ReloginFailureLimit            int      `json:"relogin_failure_limit"`
	QuotaEnabled                   bool     `json:"quota_enabled"`
	QuotaCheckIntervalSeconds      int      `json:"quota_check_interval_seconds"`
	QuotaRemainingThresholdPercent float64  `json:"quota_remaining_threshold_percent"`
	GroupIDs                       []int64  `json:"group_ids,omitempty"`
	GroupNames                     []string `json:"group_names,omitempty"`
}

func DefaultCPASettings() CPASettings {
	return CPASettings{Enable401Check: true, StatusCheckIntervalSeconds: 120, ReloginFailureLimit: 2, QuotaEnabled: true, QuotaCheckIntervalSeconds: 120}
}
