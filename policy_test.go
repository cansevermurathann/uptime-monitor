package main

import (
	"reflect"
	"testing"
	"time"
)

func TestCheckPolicyBoundariesAndPrecedence(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	boundary := now.Add(2 * time.Hour)
	afterBoundary := boundary.Add(time.Nanosecond)
	soon := now.Add(time.Hour)
	matched, mismatched := true, false
	for _, tc := range []struct {
		name   string
		policy checkPolicy
		result CheckResult
		state  string
	}{
		{"default success", checkPolicy{}, CheckResult{State: "UP", Status: 200}, "UP"},
		{"default no content", checkPolicy{}, CheckResult{State: "UP", Status: 204}, "UP"},
		{"default redirect", checkPolicy{}, CheckResult{State: "UP", Status: 302}, "DOWN"},
		{"default server error", checkPolicy{}, CheckResult{State: "UP", Status: 503}, "DOWN"},
		{"explicit redirect accepted", checkPolicy{expectedStatus: 302}, CheckResult{State: "UP", Status: 302}, "UP"},
		{"explicit status mismatch", checkPolicy{expectedStatus: 204}, CheckResult{State: "UP", Status: 200}, "DOWN"},
		{"body matches", checkPolicy{}, CheckResult{State: "UP", Status: 200, BodyMatches: &matched}, "UP"},
		{"exact header budget passes", checkPolicy{maxLatency: 500 * time.Millisecond}, CheckResult{State: "UP", Status: 200, DurationMS: 500}, "UP"},
		{"header above budget", checkPolicy{maxLatency: 500 * time.Millisecond}, CheckResult{State: "UP", Status: 200, DurationMS: 500.001}, "SLOW"},
		{"exact total budget passes", checkPolicy{maxTotalLatency: 500 * time.Millisecond}, CheckResult{State: "UP", Status: 200, TotalDurationMS: 500}, "UP"},
		{"total above budget", checkPolicy{maxTotalLatency: 500 * time.Millisecond}, CheckResult{State: "UP", Status: 200, TotalDurationMS: 500.001}, "SLOW"},
		{"explicit non-2xx still checks latency", checkPolicy{expectedStatus: 503, maxLatency: 500 * time.Millisecond}, CheckResult{State: "UP", Status: 503, DurationMS: 501}, "SLOW"},
		{"wrong status before body", checkPolicy{expectedStatus: 204}, CheckResult{State: "UP", Status: 200, BodyMatches: &mismatched}, "DOWN"},
		{"body before latency and expiry", checkPolicy{maxLatency: 500 * time.Millisecond, certExpiry: 2 * time.Hour},
			CheckResult{State: "UP", Status: 200, BodyMatches: &mismatched, DurationMS: 501, TLSExpiresAt: &soon}, "MISMATCH"},
		{"latency before expiry", checkPolicy{maxTotalLatency: 500 * time.Millisecond, certExpiry: 2 * time.Hour},
			CheckResult{State: "UP", Status: 200, TotalDurationMS: 501, TLSExpiresAt: &soon}, "SLOW"},
		{"exact expiry boundary warns", checkPolicy{certExpiry: 2 * time.Hour}, CheckResult{State: "UP", Status: 200, TLSExpiresAt: &boundary}, "EXPIRING"},
		{"after expiry boundary passes", checkPolicy{certExpiry: 2 * time.Hour}, CheckResult{State: "UP", Status: 200, TLSExpiresAt: &afterBoundary}, "UP"},
		{"expiry disabled", checkPolicy{}, CheckResult{State: "UP", Status: 200, TLSExpiresAt: &soon}, "UP"},
		{"non-TLS skips expiry", checkPolicy{certExpiry: 2 * time.Hour}, CheckResult{State: "UP", Status: 200}, "UP"},
		{"body-read error cannot be revived", checkPolicy{expectedStatus: 200, maxTotalLatency: time.Nanosecond},
			CheckResult{State: "DOWN", Status: 200, Error: "response body read failed", ErrorKind: "request", TotalDurationMS: 600}, "DOWN"},
		{"canceled observation retained", checkPolicy{expectedStatus: 200},
			CheckResult{State: "DOWN", Status: 200, Error: "response body read canceled", ErrorKind: "canceled"}, "DOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalState := tc.result.State
			tc.result.URL = "https://example.test/health"
			want := tc.result
			want.State = tc.state
			got := tc.policy.apply(tc.result, now)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v, want %+v", got, want)
			}
			if tc.result.State != originalState {
				t.Fatal("policy mutated input state")
			}
		})
	}
}
