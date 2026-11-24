package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCertificateExpiryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, window, state string
		extra               []string
		status, exit        int
		expired, untrusted  bool
	}{
		{name: "disabled by default", state: "UP", status: 204},
		{name: "disabled explicitly", window: "0", state: "UP", status: 204},
		{name: "inside window", window: "2h", state: "EXPIRING", status: 204, exit: 1},
		{name: "outside window", window: "30m", state: "UP", status: 204},
		{name: "expected status", window: "2h", state: "EXPIRING", status: 404, exit: 1, extra: []string{"-expect-status", "404"}},
		{name: "HTTP failure takes precedence", window: "2h", state: "DOWN", status: 503, exit: 1},
		{name: "latency takes precedence", window: "2h", state: "SLOW", status: 204, exit: 1, extra: []string{"-max-latency", "1ns"}},
		{name: "expired certificate stays DOWN", window: "2h", state: "DOWN", status: 204, expired: true, exit: 1},
		{name: "untrusted certificate stays DOWN", window: "2h", state: "DOWN", status: 204, untrusted: true, exit: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certificate, bundle := testCertificate(t, false, tc.expired)
			parsed, err := x509.ParseCertificate(certificate.Certificate[0])
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
			server.StartTLS()
			defer server.Close()
			for _, jsonOutput := range []bool{false, true} {
				args := append([]string{}, tc.extra...)
				if !tc.untrusted {
					args = append(args, "-ca-file", writeCA(t, bundle))
				}
				if tc.window != "" {
					args = append(args, "-cert-expiry", tc.window)
				}
				if jsonOutput {
					args = append(args, "-json")
				}
				args = append(args, server.URL)
				var out, diagnostics bytes.Buffer
				if code := run(args, &out, &diagnostics); code != tc.exit {
					t.Fatalf("exit=%d want=%d out=%s diagnostics=%s", code, tc.exit, &out, &diagnostics)
				}
				handshakeFailed := tc.expired || tc.untrusted
				if jsonOutput {
					var result CheckResult
					if err := json.Unmarshal(out.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if result.State != tc.state {
						t.Fatalf("state=%s want=%s", result.State, tc.state)
					}
					if handshakeFailed {
						if result.TLSExpiresAt != nil || result.ErrorKind != "tls_verification" || result.Status != 0 {
							t.Fatalf("unverified certificate reported: %+v", result)
						}
					} else if result.TLSExpiresAt == nil || !result.TLSExpiresAt.Equal(parsed.NotAfter) ||
						result.Status != tc.status || result.ErrorKind != "" {
						t.Fatalf("lost HTTPS result: %+v", result)
					}
				} else {
					if !strings.HasPrefix(out.String(), tc.state+" ") {
						t.Fatal(out.String())
					}
					if handshakeFailed {
						if strings.Contains(out.String(), "tls_expires_at") {
							t.Fatal(out.String())
						}
					} else if !strings.Contains(out.String(), "tls_expires_at="+parsed.NotAfter.UTC().Format(time.RFC3339)) {
						t.Fatal(out.String())
					}
				}
			}
		})
	}
}

func TestCertificateExpiryHTTPAndInvalidConfiguration(t *testing.T) {
	requests := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	code := run([]string{"-cert-expiry", "-1h", server.URL}, &out, &diagnostics)
	if code != 2 || len(requests) != 0 || out.Len() != 0 ||
		!strings.Contains(diagnostics.String(), "cert-expiry must be non-negative") {
		t.Fatalf("invalid configuration: exit=%d out=%s diagnostics=%s", code, &out, &diagnostics)
	}
	out.Reset()
	diagnostics.Reset()
	code = run([]string{"-cert-expiry", "168h", "-json", server.URL}, &out, &diagnostics)
	if code != 0 || strings.Contains(out.String(), "tls_expires_at") || !strings.Contains(out.String(), `"state":"UP"`) {
		t.Fatalf("plain HTTP affected: exit=%d out=%s diagnostics=%s", code, &out, &diagnostics)
	}
}
