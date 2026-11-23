package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type errorTransport func(*http.Request) (*http.Response, error)

func (f errorTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestRequestErrorCategoriesDoNotExposeUnderlyingDetails(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, tc := range []struct {
		name, kind, message string
		err                 error
		ctx                 context.Context
	}{
		{"DNS", "dns", "request failed", &net.DNSError{Err: "secret-token", Name: "secret-host"}, context.Background()},
		{"DNS timeout", "timeout", "request timed out", &net.DNSError{Err: "secret-token", IsTimeout: true}, context.Background()},
		{"TLS", "tls_verification", "request failed", &tls.CertificateVerificationError{Err: errors.New("secret-token")}, context.Background()},
		{"network", "network", "request failed", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("secret-token")}, context.Background()},
		{"fallback", "request", "request failed", errors.New("secret-token"), context.Background()},
		{"request timeout", "timeout", "request timed out", context.DeadlineExceeded, context.Background()},
		{"batch budget", "batch_deadline", "request timed out", context.DeadlineExceeded, expired},
		{"other failure after batch expiry", "network", "request failed", &net.OpError{Op: "read", Err: errors.New("secret-token")}, expired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: errorTransport(func(*http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("wrapped secret-token: %w", tc.err)
			})}
			result := check(tc.ctx, client, "https://example.test/health?token=secret-token", http.MethodGet)
			if result.State != "DOWN" || result.Status != 0 || result.ErrorKind != tc.kind || result.Error != tc.message {
				t.Fatalf("unexpected result: %+v", result)
			}
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "secret") {
				t.Fatalf("underlying error details exposed: %s", data)
			}
		})
	}
}

func TestHTTPResponsesOmitRequestErrorCategory(t *testing.T) {
	for _, status := range []int{204, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		for _, format := range [][]string{nil, {"-json"}} {
			var out, diagnostics bytes.Buffer
			args := append(append([]string{}, format...), server.URL)
			code := run(args, &out, &diagnostics)
			want := 0
			if status == 503 {
				want = 1
			}
			if code != want || strings.Contains(out.String(), "error_kind") {
				t.Fatalf("HTTP response mislabeled: exit=%d out=%s diagnostics=%s", code, &out, &diagnostics)
			}
		}
		server.Close()
	}
}
