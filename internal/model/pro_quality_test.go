package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestProQualityLegacySettingsDefaultFailureLimit(t *testing.T) {
	q := DefaultProQualitySettings()
	if err := json.Unmarshal([]byte(`{"enabled":true,"poll_interval_seconds":60,"action":"merge"}`), &q); err != nil {
		t.Fatal(err)
	}
	if q.ErrorFailureLimit != 2 || q.ConnectionFailureHTTPStatuses != "429,502,503" || q.Validate() != nil {
		t.Fatalf("old settings lost default threshold: %+v", q)
	}
}

func TestProQualityConnectionHTTPStatuses(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []int
	}{
		{"", []int{429, 502, 503}}, {"429,502,503", []int{429, 502, 503}}, {" 504, 429 ,504 ", []int{504, 429}}, {"400,599", []int{400, 599}},
	} {
		q := DefaultProQualitySettings()
		q.ConnectionFailureHTTPStatuses = tc.raw
		got, err := q.ConnectionFailureStatuses()
		if err != nil || !reflect.DeepEqual(got, tc.want) || q.Validate() != nil {
			t.Fatalf("%q: %v %v", tc.raw, got, err)
		}
		if q.ConnectionFailureMatches(200) || q.ConnectionFailureMatches(0) {
			t.Fatal("invalid status matched")
		}
		for _, code := range tc.want {
			if !q.ConnectionFailureMatches(code) {
				t.Fatalf("missing %d", code)
			}
		}
	}
	for _, raw := range []string{"200", "399", "600", "429,", "429,,503", "429，503", "nope", "0502", "+502", "429 503"} {
		q := DefaultProQualitySettings()
		q.ConnectionFailureHTTPStatuses = raw
		if q.Validate() == nil || q.ConnectionFailureMatches(429) {
			t.Fatalf("invalid config accepted: %q", raw)
		}
	}
}
