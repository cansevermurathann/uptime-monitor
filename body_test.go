package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponseBodyAssertions(t *testing.T) {
	for _, tc := range []struct {
		name, body, expected, state string
		status, exit                int
		extra                       []string
		matches                     bool
	}{
		{"match", "prefix secret-marker secret-response", "secret-marker", "UP", 200, 0, nil, true},
		{"missing", "degraded", "secret-marker", "MISMATCH", 200, 1, nil, false},
		{"case sensitive", "HEALTHY", "healthy", "MISMATCH", 200, 1, nil, false},
		{"status failure", "healthy", "healthy", "DOWN", 503, 1, nil, true},
		{"expected status", "healthy", "healthy", "UP", 503, 0, []string{"-expect-status", "503"}, true},
		{"match ends at limit", strings.Repeat("x", 64*1024-2) + "OK", "OK", "UP", 200, 0, nil, true},
		{"match beyond limit", strings.Repeat("x", 64*1024) + "OK", "OK", "MISMATCH", 200, 1, nil, false},
		{"match crosses limit", strings.Repeat("x", 64*1024-1) + "OK", "OK", "MISMATCH", 200, 1, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			for _, jsonOutput := range []bool{false, true} {
				args := append([]string{"-expect-body", tc.expected}, tc.extra...)
				if jsonOutput {
					args = append(args, "-json")
				}
				args = append(args, server.URL+"?token=secret-query")
				var out, diagnostics bytes.Buffer
				if code := run(args, &out, &diagnostics); code != tc.exit {
					t.Fatalf("exit=%d out=%s diagnostics=%s", code, &out, &diagnostics)
				}
				if strings.Contains(out.String()+diagnostics.String(), "secret") {
					t.Fatalf("body, expectation or query leaked: %s %s", &out, &diagnostics)
				}
				if jsonOutput {
					var result CheckResult
					if err := json.Unmarshal(out.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if result.State != tc.state || result.Status != tc.status ||
						result.BodyMatches == nil || *result.BodyMatches != tc.matches || result.ErrorKind != "" {
						t.Fatalf("result=%+v", result)
					}
				} else if !strings.HasPrefix(out.String(), tc.state+" ") {
					t.Fatal(out.String())
				}
			}
		})
	}
}

func TestResponseBodyReadTimeoutsAndContinuedChecks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "healthy")
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, kind string
		args       []string
	}{
		{"request timeout", "timeout", []string{"-timeout", "100ms"}},
		{"batch timeout", "batch_deadline", []string{"-deadline", "100ms", "-timeout", "5s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			args := append([]string{"-expect-body", "healthy", "-json", "-concurrency", "2"}, tc.args...)
			args = append(args, server.URL+"/slow", server.URL+"/fast")
			if code := run(args, &out, &diagnostics); code != 1 {
				t.Fatalf("exit=%d diagnostics=%s", code, &diagnostics)
			}
			decoder := json.NewDecoder(&out)
			var slow, fast CheckResult
			if err := decoder.Decode(&slow); err != nil {
				t.Fatal(err)
			}
			if err := decoder.Decode(&fast); err != nil {
				t.Fatal(err)
			}
			if slow.State != "DOWN" || slow.Status != 200 || slow.ErrorKind != tc.kind ||
				slow.Error != "response body read timed out" || slow.BodyMatches != nil {
				t.Fatalf("slow=%+v", slow)
			}
			if fast.State != "UP" || fast.BodyMatches == nil || !*fast.BodyMatches {
				t.Fatalf("fast=%+v", fast)
			}
		})
	}
}

func TestTruncatedBodyPreservesReceivedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		io.WriteString(w, "partial")
	}))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	code := run([]string{"-expect-body", "partial", server.URL}, &out, &diagnostics)
	if code != 1 || !strings.Contains(out.String(), "DOWN status=200") ||
		!strings.Contains(out.String(), "response body read failed") {
		t.Fatalf("exit=%d out=%s diagnostics=%s", code, &out, &diagnostics)
	}
}

func TestInvalidBodyConfigurationDoesNotSendRequests(t *testing.T) {
	requests := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests <- struct{}{} }))
	defer server.Close()
	for _, args := range [][]string{
		{"-method", "HEAD", "-expect-body", "healthy"},
		{"-expect-body", strings.Repeat("x", 64*1024+1)},
	} {
		var out, diagnostics bytes.Buffer
		code := run(append(args, server.URL), &out, &diagnostics)
		if code != 2 || out.Len() != 0 || diagnostics.Len() == 0 || len(requests) != 0 {
			t.Fatalf("exit=%d out=%s diagnostics=%s", code, &out, &diagnostics)
		}
	}
}
