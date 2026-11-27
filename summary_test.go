package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type summaryWriteFailure struct{}

func (summaryWriteFailure) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestSummaryWithMixedResultsKeepsPerTargetOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(503)
		}
		if r.URL.Path == "/wrong" {
			fmt.Fprint(w, "degraded")
			return
		}
		fmt.Fprint(w, "secret-marker")
	}))
	defer server.Close()
	for _, jsonOutput := range []bool{false, true} {
		var out, diagnostics bytes.Buffer
		args := []string{"-summary", "-expect-body", "secret-marker", "-concurrency", "2"}
		if jsonOutput {
			args = append(args, "-json")
		}
		paths := []string{"/fast", "/bad", "/wrong", "/fast"}
		for _, path := range paths {
			args = append(args, server.URL+path+"?token=secret-query")
		}
		code := run(args, &out, &diagnostics)
		wantSummary := "SUMMARY total=4 up=2 down=1 slow=0 mismatch=1 expiring=0 canceled=0\n"
		if code != 1 || diagnostics.String() != wantSummary {
			t.Fatalf("exit=%d diagnostics=%s", code, &diagnostics)
		}
		if strings.Contains(out.String()+diagnostics.String(), "secret") {
			t.Fatal("response, expectation or query leaked")
		}
		if jsonOutput {
			decoder := json.NewDecoder(&out)
			for i, wantState := range []string{"UP", "DOWN", "MISMATCH", "UP"} {
				var result CheckResult
				if err := decoder.Decode(&result); err != nil {
					t.Fatal(err)
				}
				if result.State != wantState || result.URL != server.URL+paths[i] {
					t.Fatalf("result=%+v", result)
				}
			}
			var extra CheckResult
			if err := decoder.Decode(&extra); err != io.EOF {
				t.Fatalf("summary contaminated JSON: %v", err)
			}
		} else if strings.Count(out.String(), "\n") != 4 || strings.Contains(out.String(), "SUMMARY") {
			t.Fatal(out.String())
		}
	}
}

func TestSummaryCoversAllStatesWithoutSensitiveDetails(t *testing.T) {
	results := []CheckResult{
		{State: "UP", URL: "https://secret.test"},
		{State: "UP"},
		{State: "DOWN", Error: "secret-error"},
		{State: "DOWN", ErrorKind: "canceled"},
		{State: "SLOW"},
		{State: "MISMATCH"},
		{State: "EXPIRING"},
	}
	var out bytes.Buffer
	if err := writeSummary(&out, results); err != nil {
		t.Fatal(err)
	}
	want := "SUMMARY total=7 up=2 down=2 slow=1 mismatch=1 expiring=1 canceled=1\n"
	if out.String() != want {
		t.Fatal(out.String())
	}
}

func TestSummaryOnCanceledBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, diagnostics bytes.Buffer
	code := runWithContext(ctx, []string{"-summary", "-json", "http://127.0.0.1:1"},
		strings.NewReader(""), &out, &diagnostics)
	if code != 130 || diagnostics.String() != "SUMMARY total=1 up=0 down=1 slow=0 mismatch=0 expiring=0 canceled=1\n" {
		t.Fatalf("exit=%d out=%s diagnostics=%s", code, &out, &diagnostics)
	}
}

func TestSummaryDefaultsAndWriteFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	if code := run([]string{server.URL}, &out, &diagnostics); code != 0 || diagnostics.Len() != 0 {
		t.Fatalf("default summary: exit=%d diag=%s", code, &diagnostics)
	}
	out.Reset()
	if code := run([]string{"-summary", "-json", server.URL}, &out, summaryWriteFailure{}); code != 2 || out.Len() == 0 {
		t.Fatalf("summary write error: exit=%d out=%s", code, &out)
	}
	diagnostics.Reset()
	if code := run([]string{"-summary", server.URL}, summaryWriteFailure{}, &diagnostics); code != 2 ||
		strings.Contains(diagnostics.String(), "SUMMARY") {
		t.Fatalf("per-target write error: exit=%d diag=%s", code, &diagnostics)
	}
	out.Reset()
	diagnostics.Reset()
	if code := run([]string{"-summary", "ftp://example.test"}, &out, &diagnostics); code != 2 ||
		out.Len() != 0 || strings.Contains(diagnostics.String(), "SUMMARY") {
		t.Fatalf("invalid configuration: exit=%d out=%s diag=%s", code, &out, &diagnostics)
	}
}
