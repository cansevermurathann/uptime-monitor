package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Certificates and keys are generated per test and never written to the repo.
func testCertificate(t *testing.T, wrongHost, expired bool) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-2 * time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	if wrongHost {
		template.IPAddresses = []net.IP{net.ParseIP("192.0.2.1")}
	}
	if expired {
		template.NotAfter = time.Now().Add(-time.Hour)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeCA(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPrivateCATrustAndVerification(t *testing.T) {
	for _, tc := range []struct {
		name, method                          string
		noCA, unrelatedCA, wrongHost, expired bool
		wantExit                              int
	}{
		{name: "untrusted by default", noCA: true, wantExit: 1},
		{name: "trusted GET"},
		{name: "trusted HEAD", method: "HEAD"},
		{name: "unrelated CA", unrelatedCA: true, wantExit: 1},
		{name: "hostname still checked", wrongHost: true, wantExit: 1},
		{name: "expiration still checked", expired: true, wantExit: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certificate, bundle := testCertificate(t, tc.wrongHost, tc.expired)
			if tc.unrelatedCA {
				_, bundle = testCertificate(t, false, false)
			}
			requests := make(chan string, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Method
				w.WriteHeader(http.StatusNoContent)
			}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
			server.StartTLS()
			defer server.Close()
			args := []string{"-json", "-timeout", "2s"}
			if !tc.noCA {
				args = append(args, "-ca-file", writeCA(t, bundle))
			}
			if tc.method != "" {
				args = append(args, "-method", tc.method)
			}
			args = append(args, server.URL)
			var out, diagnostics bytes.Buffer
			code := run(args, &out, &diagnostics)
			if code != tc.wantExit {
				t.Fatalf("exit=%d want=%d diagnostics=%s output=%s", code, tc.wantExit, &diagnostics, &out)
			}
			var result CheckResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if tc.wantExit == 0 {
				if result.Status != 204 || result.State != "UP" || len(requests) != 1 {
					t.Fatalf("trusted server not reached: %+v", result)
				}
				wantMethod := tc.method
				if wantMethod == "" {
					wantMethod = "GET"
				}
				if method := <-requests; method != wantMethod {
					t.Fatalf("method=%s", method)
				}
			} else if result.Status != 0 || result.State != "DOWN" ||
				result.Error != "request failed" || result.ErrorKind != "tls_verification" || len(requests) != 0 {
				t.Fatalf("verification was bypassed: %+v", result)
			}
		})
	}
}

func TestInvalidCAFilePreventsAllRequests(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests <- struct{}{} }))
	defer server.Close()
	for _, path := range []string{
		filepath.Join(t.TempDir(), "missing.pem"),
		writeCA(t, nil),
		writeCA(t, []byte("not a certificate")),
		writeCA(t, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid DER")})),
	} {
		var out, diagnostics bytes.Buffer
		code := run([]string{"-ca-file", path, server.URL}, &out, &diagnostics)
		if code != 2 || out.Len() != 0 || diagnostics.Len() == 0 || len(requests) != 0 {
			t.Fatalf("exit=%d output=%s diagnostics=%s", code, &out, &diagnostics)
		}
	}
}

func TestAdditionalTrustIsIsolated(t *testing.T) {
	_, bundle := testCertificate(t, false, false)
	defaultTransport := http.DefaultTransport.(*http.Transport)
	originalTLSConfig := defaultTransport.TLSClientConfig
	transport, err := newTransport(writeCA(t, bundle))
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	if transport == defaultTransport || transport.TLSClientConfig == originalTLSConfig ||
		transport.TLSClientConfig.RootCAs == nil || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("custom roots must use an isolated, verifying transport")
	}
	if defaultTransport.TLSClientConfig != originalTLSConfig {
		t.Fatal("global transport was modified")
	}
	normal, err := newTransport("")
	if err != nil {
		t.Fatal(err)
	}
	defer normal.CloseIdleConnections()
	if normal.TLSClientConfig != nil && normal.TLSClientConfig.RootCAs != nil {
		t.Fatal("custom roots leaked into a subsequent invocation")
	}
}
