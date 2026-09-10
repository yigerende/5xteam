package workflow

import (
	"net/http"
	"testing"

	"chatgpt-space-merge/internal/model"
)

func TestClassifyOpenAIProxyResponse(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		headers    http.Header
		body       string
		wantStatus string
		wantRay    string
	}{
		{name: "unauthorized is reachable", statusCode: http.StatusUnauthorized, wantStatus: "pass"},
		{name: "rate limit is warning", statusCode: http.StatusTooManyRequests, wantStatus: "warn"},
		{name: "unexpected forbidden fails", statusCode: http.StatusForbidden, wantStatus: "fail"},
		{name: "cloudflare header is challenge", statusCode: http.StatusForbidden, headers: http.Header{"Cf-Mitigated": []string{"challenge"}, "Cf-Ray": []string{"ray-123"}}, wantStatus: "challenge", wantRay: "ray-123"},
		{name: "cloudflare html is challenge", statusCode: http.StatusForbidden, headers: http.Header{"Content-Type": []string{"text/html"}}, body: "<!doctype html><title>Just a moment...</title><script>window._cf_chl_opt={}</script>", wantStatus: "challenge"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, _, ray := classifyOpenAIProxyResponse(test.statusCode, test.headers, []byte(test.body))
			if status != test.wantStatus || ray != test.wantRay {
				t.Fatalf("status=%q ray=%q, want status=%q ray=%q", status, ray, test.wantStatus, test.wantRay)
			}
		})
	}
}

func TestFinalizeProxyOpenAIQualityUsesSub2APIScoring(t *testing.T) {
	tests := []struct {
		status    string
		wantScore int
		wantGrade string
	}{{"pass", 100, "A"}, {"warn", 90, "A"}, {"fail", 78, "B"}, {"challenge", 70, "C"}}
	for _, test := range tests {
		result := model.ProxyOpenAIQualityResult{Status: test.status}
		finalizeProxyOpenAIQuality(&result)
		if result.Score != test.wantScore || result.Grade != test.wantGrade {
			t.Fatalf("status=%s score=%d grade=%s", test.status, result.Score, result.Grade)
		}
	}
}

func TestParseIPAPIExit(t *testing.T) {
	result, err := parseIPAPIExit([]byte(`{"status":"success","query":"203.0.113.7","country":"United States","countryCode":"US","regionName":"California","city":"Los Angeles"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IP != "203.0.113.7" || result.CountryCode != "US" || result.Region != "California" || result.City != "Los Angeles" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
