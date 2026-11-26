package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testClientIdentity(t *testing.T, expired bool) (certPath, keyPath string, certificate *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "uptime-test-client"},
		NotBefore:             time.Now().Add(-2 * time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if expired {
		template.NotAfter = time.Now().Add(-time.Hour)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "client.pem"), filepath.Join(dir, "client-key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	certificate, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestMutualTLSClientAuthentication(t *testing.T) {
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		for _, tc := range []struct {
			name, method                                       string
			missing, untrusted, expired, wrongHost, noServerCA bool
			wantExit                                           int
		}{
			{name: "trusted GET"},
			{name: "trusted HEAD", method: "HEAD"},
			{name: "no identity", missing: true, wantExit: 1},
			{name: "untrusted identity", untrusted: true, wantExit: 1},
			{name: "expired identity", expired: true, wantExit: 1},
			{name: "server hostname still verified", wrongHost: true, wantExit: 1},
			{name: "server trust still required", noServerCA: true, wantExit: 1},
		} {
			t.Run(tls.VersionName(version)+"/"+tc.name, func(t *testing.T) {
				certPath, keyPath, trustedClient := testClientIdentity(t, tc.expired)
				clientRoots := x509.NewCertPool()
				clientRoots.AddCert(trustedClient)
				if tc.untrusted {
					certPath, keyPath, _ = testClientIdentity(t, false)
				}
				serverCertificate, serverPEM := testCertificate(t, tc.wrongHost, false)
				received := make(chan string, 1)
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
						t.Error("client identity was not verified")
					}
					received <- r.Method
					w.WriteHeader(http.StatusNoContent)
				}))
				server.Config.ErrorLog = log.New(io.Discard, "", 0)
				server.TLS = &tls.Config{Certificates: []tls.Certificate{serverCertificate},
					ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientRoots, MinVersion: version, MaxVersion: version}
				server.StartTLS()
				defer server.Close()
				args := []string{"-json", "-timeout", "2s"}
				if !tc.noServerCA {
					args = append(args, "-ca-file", writeCA(t, serverPEM))
				}
				if !tc.missing {
					args = append(args, "-client-cert", certPath, "-client-key", keyPath)
				}
				if tc.method != "" {
					args = append(args, "-method", tc.method)
				}
				args = append(args, server.URL)
				var out, diagnostics bytes.Buffer
				code := run(args, &out, &diagnostics)
				var result CheckResult
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatalf("%v out=%s diag=%s", err, &out, &diagnostics)
				}
				if code != tc.wantExit {
					t.Fatalf("exit=%d result=%+v diag=%s", code, result, &diagnostics)
				}
				if tc.wantExit == 0 {
					if result.State != "UP" || result.Status != 204 || len(received) != 1 {
						t.Fatalf("result=%+v", result)
					}
					wantMethod := tc.method
					if wantMethod == "" {
						wantMethod = "GET"
					}
					if method := <-received; method != wantMethod {
						t.Fatalf("method=%s", method)
					}
				} else if result.State != "DOWN" || result.Status != 0 || len(received) != 0 {
					t.Fatalf("rejected handshake reached handler: %+v", result)
				}
			})
		}
	}
}

func TestInvalidClientIdentityPreventsRequests(t *testing.T) {
	cert, key, _ := testClientIdentity(t, false)
	_, otherKey, _ := testClientIdentity(t, false)
	bad := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(bad, []byte("invalid PEM"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := make(chan struct{}, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests <- struct{}{} }))
	defer server.Close()
	for _, args := range [][]string{
		{"-client-cert", cert},
		{"-client-key", key},
		{"-client-cert", cert, "-client-key", otherKey},
		{"-client-cert", bad, "-client-key", key},
		{"-client-cert", cert, "-client-key", bad},
		{"-client-cert", cert, "-client-key", filepath.Join(t.TempDir(), "missing.pem")},
	} {
		var out, diagnostics bytes.Buffer
		code := run(append(args, server.URL), &out, &diagnostics)
		if code != 2 || out.Len() != 0 || diagnostics.Len() == 0 || len(requests) != 0 {
			t.Fatalf("exit=%d out=%s diag=%s", code, &out, &diagnostics)
		}
	}
}

func TestClientIdentityDoesNotChangeServerTrustOrLeak(t *testing.T) {
	cert, key, _ := testClientIdentity(t, false)
	transport, err := newTransport("", cert, key)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	if transport.TLSClientConfig.RootCAs != nil || transport.TLSClientConfig.InsecureSkipVerify ||
		len(transport.TLSClientConfig.Certificates) != 1 {
		t.Fatal("client identity changed server verification")
	}
	normal, err := newTransport("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer normal.CloseIdleConnections()
	if normal.TLSClientConfig != nil && len(normal.TLSClientConfig.Certificates) != 0 {
		t.Fatal("identity leaked into subsequent invocation")
	}
}
