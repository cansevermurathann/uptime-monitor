package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCancellationPreservesCompletedAndStopsQueuedTargets(t *testing.T) {
	for _, bodyRead := range []bool{false, true} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("body=%t/json=%t", bodyRead, jsonOutput), func(t *testing.T) {
				started := make(chan struct{})
				observed := make(chan struct{})
				requests := make(chan string, 4)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests <- r.URL.Path
					if r.URL.Path == "/slow" {
						if bodyRead {
							w.WriteHeader(200)
							w.(http.Flusher).Flush()
						}
						close(started)
						<-r.Context().Done()
						close(observed)
						return
					}
					fmt.Fprint(w, "healthy")
				}))
				defer server.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				args := []string{"-timeout", "30s", "-deadline", "1m"}
				if bodyRead {
					args = append(args, "-expect-body", "healthy")
				}
				if jsonOutput {
					args = append(args, "-json")
				}
				args = append(args, server.URL+"/fast", server.URL+"/slow", server.URL+"/queued")
				var out, diagnostics bytes.Buffer
				done := make(chan int, 1)
				go func() { done <- runWithContext(ctx, args, strings.NewReader(""), &out, &diagnostics) }()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("slow request did not start")
				}
				cancel()
				select {
				case code := <-done:
					if code != 130 {
						t.Fatalf("exit=%d diag=%s out=%s", code, &diagnostics, &out)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("cancellation did not finish the batch")
				}
				select {
				case <-observed:
				case <-time.After(time.Second):
					t.Fatal("server did not observe cancellation")
				}
				if len(requests) != 2 {
					t.Fatalf("queued target reached server: requests=%d", len(requests))
				}
				if jsonOutput {
					decoder := json.NewDecoder(&out)
					for index, path := range []string{"/fast", "/slow", "/queued"} {
						var result CheckResult
						if err := decoder.Decode(&result); err != nil {
							t.Fatal(err)
						}
						if result.URL != server.URL+path {
							t.Fatalf("order: %+v", result)
						}
						if index == 0 {
							if result.State != "UP" || result.Status != 200 {
								t.Fatalf("lost completed result: %+v", result)
							}
						} else {
							if result.State != "DOWN" || result.ErrorKind != "canceled" {
								t.Fatalf("canceled result: %+v", result)
							}
							// Cancellation can race delivery of flushed headers.
							// Deterministic body-read coverage is tested separately.
							if result.Status != 0 && !(index == 1 && bodyRead && result.Status == 200) {
								t.Fatalf("unexpected status: %+v", result)
							}
						}
					}
					var extra CheckResult
					if err := decoder.Decode(&extra); err != io.EOF {
						t.Fatalf("extra output: %v", err)
					}
				} else if !strings.Contains(out.String(), "UP status=200") ||
					strings.Count(out.String(), "error_kind=canceled") != 2 ||
					strings.Count(out.String(), "\n") != 3 {
					t.Fatal(out.String())
				}
			})
		}
	}
}

func TestAlreadyCanceledContextDoesNotSendRequests(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests <- struct{}{} }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, diagnostics bytes.Buffer
	code := runWithContext(ctx, []string{"-json", server.URL}, strings.NewReader(""), &out, &diagnostics)
	if code != 130 || len(requests) != 0 || !strings.Contains(out.String(), `"error_kind":"canceled"`) {
		t.Fatalf("exit=%d out=%s diag=%s", code, &out, &diagnostics)
	}
}

type cancellationBody struct {
	ctx     context.Context
	started chan struct{}
	closed  bool
}

func (b *cancellationBody) Read([]byte) (int, error) {
	close(b.started)
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *cancellationBody) Close() error { b.closed = true; return nil }

func TestCancellationDuringBodyReadPreservesStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &cancellationBody{ctx: ctx, started: make(chan struct{})}
	client := &http.Client{Transport: errorTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header), Request: r}, nil
	})}
	done := make(chan CheckResult, 1)
	go func() { done <- check(ctx, client, "https://example.test/health", "GET", "healthy", nil) }()
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("body read did not start")
	}
	cancel()
	select {
	case result := <-done:
		if result.Status != 200 || result.State != "DOWN" || result.ErrorKind != "canceled" ||
			result.Error != "response body read canceled" || result.BodyMatches != nil || !body.closed {
			t.Fatalf("result=%+v closed=%t", result, body.closed)
		}
	case <-time.After(time.Second):
		t.Fatal("body read did not cancel")
	}
}
