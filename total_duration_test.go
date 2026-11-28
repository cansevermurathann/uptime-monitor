package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTotalBudgetDetectsSlowBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-time.After(100 * time.Millisecond):
			fmt.Fprint(w, "healthy")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		budget, state string
		exit          int
	}{
		{"0", "UP", 0},
		{"50ms", "SLOW", 1},
		{"5s", "UP", 0},
	} {
		var out, diagnostics bytes.Buffer
		code := run([]string{"-expect-body", "healthy", "-max-total-latency", tc.budget,
			"-max-latency", "5s", "-timeout", "5s", "-json", server.URL}, &out, &diagnostics)
		var result CheckResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if code != tc.exit || result.State != tc.state || result.BodyMatches == nil || !*result.BodyMatches {
			t.Fatalf("budget=%s exit=%d result=%+v diagnostics=%s", tc.budget, code, result, &diagnostics)
		}
		if result.TotalDurationMS-result.DurationMS < 70 {
			t.Fatalf("body-read time missing: %+v", result)
		}
	}
}

func TestTotalDurationRecordedForBodyReadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	code := run([]string{"-expect-body", "healthy", "-timeout", "100ms",
		"-max-total-latency", "1ms", "-json", server.URL}, &out, &diagnostics)
	var result CheckResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if code != 1 || result.State != "DOWN" || result.ErrorKind != "timeout" ||
		result.TotalDurationMS < 70 || result.TotalDurationMS < result.DurationMS {
		t.Fatalf("exit=%d result=%+v diagnostics=%s", code, result, &diagnostics)
	}
}

func TestMismatchReportsTotalTextTiming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "degraded") }))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	code := run([]string{"-expect-body", "healthy", server.URL}, &out, &diagnostics)
	if code != 1 || !strings.HasPrefix(out.String(), "MISMATCH ") || !strings.Contains(out.String(), "total_duration=") {
		t.Fatalf("exit=%d out=%s diagnostics=%s", code, &out, &diagnostics)
	}
}

func TestNegativeTotalBudgetPreventsRequests(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests <- struct{}{} }))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	code := run([]string{"-max-total-latency", "-1s", server.URL}, &out, &diagnostics)
	if code != 2 || out.Len() != 0 || len(requests) != 0 || diagnostics.Len() == 0 {
		t.Fatalf("exit=%d out=%s diagnostics=%s", code, &out, &diagnostics)
	}
}
