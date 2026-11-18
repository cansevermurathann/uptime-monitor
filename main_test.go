package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusesAndContinuedChecks(t *testing.T) {
	for _, status := range []int{200, 204, 302, 404, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/other")
				w.WriteHeader(status)
			}))
			defer server.Close()
			var out, diagnostics bytes.Buffer
			code := run([]string{server.URL}, &out, &diagnostics)
			want := 1
			if status >= 200 && status < 300 {
				want = 0
			}
			if code != want || !strings.Contains(out.String(), "duration=") {
				t.Fatalf("exit=%d output=%s diagnostic=%s", code, &out, &diagnostics)
			}
		})
	}
}

func TestTimeoutAndNextTarget(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer fast.Close()
	var out, diagnostics bytes.Buffer
	if code := run([]string{"-timeout", "100ms", slow.URL, fast.URL}, &out, &diagnostics); code != 1 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(out.String(), "DOWN status=0") || !strings.Contains(out.String(), "UP status=204") {
		t.Fatal(out.String())
	}
}

func TestConfiguration(t *testing.T) {
	for _, args := range [][]string{nil, {"-timeout", "0", "http://localhost"}, {"ftp://localhost"}, {"http://user:pass@localhost"}} {
		var out, diagnostics bytes.Buffer
		if run(args, &out, &diagnostics) != 2 || out.Len() != 0 {
			t.Fatal(args)
		}
	}
}

func TestJSONOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	if run([]string{"-json", server.URL, server.URL + "/bad"}, &out, &diagnostics) != 1 {
		t.Fatal("expected failed check")
	}
	decoder := json.NewDecoder(&out)
	for _, status := range []int{204, 503} {
		var result CheckResult
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.Status != status || result.DurationMS < 0 || result.Error != "" {
			t.Fatalf("%+v", result)
		}
	}
	var extra CheckResult
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("unexpected output: %v", err)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write(p []byte) (int, error) { return 0, errors.New("closed output") }
func TestOutputFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	for _, args := range [][]string{{server.URL}, {"-json", server.URL}} {
		var diagnostics bytes.Buffer
		if run(args, brokenWriter{}, &diagnostics) != 2 || !strings.Contains(diagnostics.String(), "write results") {
			t.Fatal(diagnostics.String())
		}
	}
}
