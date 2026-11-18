package main

import (
	"bytes"
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
