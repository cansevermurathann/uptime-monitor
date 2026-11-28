package main

import "time"

type checkPolicy struct {
	expectedStatus  int
	maxLatency      time.Duration
	maxTotalLatency time.Duration
	certExpiry      time.Duration
}

// apply evaluates a completed observation using already validated settings.
// An explicit time keeps certificate boundaries deterministic in tests.
func (p checkPolicy) apply(result CheckResult, now time.Time) CheckResult {
	if result.Error != "" {
		return result
	}
	accepted := result.Status >= 200 && result.Status < 300
	if p.expectedStatus != 0 {
		accepted = result.Status == p.expectedStatus
	}
	if !accepted {
		result.State = "DOWN"
		return result
	}
	result.State = "UP"
	if (result.HeadersMatch != nil && !*result.HeadersMatch) ||
		(result.BodyMatches != nil && !*result.BodyMatches) {
		result.State = "MISMATCH"
		return result
	}
	if (p.maxLatency > 0 && result.DurationMS > float64(p.maxLatency)/float64(time.Millisecond)) ||
		(p.maxTotalLatency > 0 && result.TotalDurationMS > float64(p.maxTotalLatency)/float64(time.Millisecond)) {
		result.State = "SLOW"
		return result
	}
	if p.certExpiry > 0 && result.TLSExpiresAt != nil && !result.TLSExpiresAt.After(now.Add(p.certExpiry)) {
		result.State = "EXPIRING"
	}
	return result
}
