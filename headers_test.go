package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestResponseHeaderAssertions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		status int
		state  string
		code   int
		match  *bool
	}{
		{"disabled", nil, 200, "UP", 0, nil},
		{"case insensitive name", []string{"-expect-header", "content-type: application/json"}, 200, "UP", 0, boolPointer(true)},
		{"trim spaces and tabs", []string{"-expect-header", "Content-Type: \tapplication/json\t "}, 200, "UP", 0, boolPointer(true)},
		{"complete comma value", []string{"-expect-header", "Cache-Control:private, no-store"}, 200, "UP", 0, boolPointer(true)},
		{"HEAD", []string{"-method", "HEAD", "-expect-header", "Content-Type:application/json"}, 200, "UP", 0, boolPointer(true)},
		{"wrong value", []string{"-expect-header", "Content-Type:text/html"}, 200, "MISMATCH", 1, boolPointer(false)},
		{"value is case sensitive", []string{"-expect-header", "X-Deployment:BLUE"}, 200, "MISMATCH", 1, boolPointer(false)},
		{"missing", []string{"-expect-header", "X-Missing:blue"}, 200, "MISMATCH", 1, boolPointer(false)},
		{"missing is not empty", []string{"-expect-header", "X-Missing:"}, 200, "MISMATCH", 1, boolPointer(false)},
		{"present empty", []string{"-expect-header", "X-Empty:"}, 200, "UP", 0, boolPointer(true)},
		{"second field value", []string{"-expect-header", "X-Deployment:green"}, 200, "UP", 0, boolPointer(true)},
		{"all assertions", []string{"-expect-header", "X-Deployment:blue", "-expect-header", "X-Deployment:green"}, 200, "UP", 0, boolPointer(true)},
		{"one assertion fails", []string{"-expect-header", "X-Deployment:blue", "-expect-header", "X-Deployment:red"}, 200, "MISMATCH", 1, boolPointer(false)},
		{"whole value not comma token", []string{"-expect-header", "Cache-Control:no-store"}, 200, "MISMATCH", 1, boolPointer(false)},
		{"value contains colon", []string{"-expect-header", "X-Source:http://local.test"}, 200, "UP", 0, boolPointer(true)},
		{"status wins", []string{"-expect-header", "X-Missing:x"}, 503, "DOWN", 1, boolPointer(false)},
		{"explicit status still checks header", []string{"-expect-status", "503", "-expect-header", "X-Missing:x"}, 503, "MISMATCH", 1, boolPointer(false)},
		{"body still checked", []string{"-expect-header", "Content-Type:application/json", "-expect-body", "healthy"}, 200, "MISMATCH", 1, boolPointer(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Add("X-Deployment", "blue")
				w.Header().Add("X-Deployment", "green")
				w.Header().Set("X-Empty", "")
				w.Header().Set("Cache-Control", "private, no-store")
				w.Header().Set("X-Source", "http://local.test")
				w.Header().Set("X-Private", "private-response-marker")
				w.WriteHeader(tc.status)
				if r.Method != "HEAD" {
					w.Write([]byte(`{"status":"degraded"}`))
				}
			}))
			defer server.Close()
			args := append(append([]string{}, tc.args...), "-json", server.URL)
			var out, diagnostics bytes.Buffer
			code := run(args, &out, &diagnostics)
			var result CheckResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if code != tc.code || result.State != tc.state || result.Status != tc.status {
				t.Fatalf("exit=%d result=%+v", code, result)
			}
			if (result.HeadersMatch == nil) != (tc.match == nil) || (tc.match != nil && *result.HeadersMatch != *tc.match) {
				t.Fatalf("headers_match=%v want=%v", result.HeadersMatch, tc.match)
			}
			if strings.Contains(out.String()+diagnostics.String(), "private-response-marker") || strings.Contains(out.String(), "http://local.test") {
				t.Fatal("response header values leaked")
			}
			if tc.match == nil && strings.Contains(out.String(), "headers_match") {
				t.Fatal("disabled assertion emitted a field")
			}
		})
	}
}

func boolPointer(v bool) *bool { return &v }

func TestInvalidHeaderExpectationsMakeNoRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, value := range []string{"missing-colon", ":private-marker", "Bad Name:private-marker", "X-Ü:private-marker", "X-Test:private-marker\r\nInjected:yes", "X-Test:private-marker\x00", "X-Test:private-marker\x7f"} {
		var out, diagnostics bytes.Buffer
		code := run([]string{"-expect-header", value, server.URL}, &out, &diagnostics)
		if code != 2 || out.Len() != 0 || diagnostics.Len() == 0 || strings.Contains(diagnostics.String(), "private-marker") {
			t.Fatalf("exit=%d out=%q diagnostics=%q", code, &out, &diagnostics)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid configuration sent requests")
	}
}

func TestHeaderMismatchTextAndSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	code := run([]string{"-summary", "-expect-header", "X-Ready:private-expected-marker", server.URL}, &out, &diagnostics)
	if code != 1 || !strings.HasPrefix(out.String(), "MISMATCH ") || !strings.Contains(diagnostics.String(), "mismatch=1") {
		t.Fatalf("exit=%d out=%q diagnostics=%q", code, &out, &diagnostics)
	}
	if strings.Contains(out.String()+diagnostics.String(), "private-expected-marker") {
		t.Fatal("expected value leaked")
	}
}
