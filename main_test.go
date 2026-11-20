package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestBoundedConcurrencyAndOutputOrder(t *testing.T) {
	entered := make(chan string, 3)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- r.URL.Path
		<-release
		w.WriteHeader(204)
	}))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"-json", "-concurrency", "2", server.URL + "/a", server.URL + "/b", server.URL + "/c"}, &out, &diagnostics)
	}()
	// Both workers must reach the server before either is released.
	for range 2 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			close(release)
			<-done
			t.Fatal("two concurrent requests did not arrive")
		}
	}
	select {
	case <-entered:
		close(release)
		<-done
		t.Fatal("concurrency limit exceeded")
	default:
	}
	close(release)
	if code := <-done; code != 0 {
		t.Fatalf("exit=%d", code)
	}
	decoder := json.NewDecoder(&out)
	for _, path := range []string{"/a", "/b", "/c"} {
		var result CheckResult
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.URL != server.URL+path {
			t.Fatalf("unexpected order: %+v", result)
		}
	}
}

func TestInvalidConcurrency(t *testing.T) {
	for _, value := range []string{"0", "33"} {
		var out, diagnostics bytes.Buffer
		if run([]string{"-concurrency", value, "http://localhost"}, &out, &diagnostics) != 2 {
			t.Fatal(value)
		}
	}
}

func TestFileTargets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "targets.txt")
	content := "# local checks\r\n  " + server.URL + "/first  \r\n\r\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	code := run([]string{"-json", "-file", path, server.URL + "/second"}, &out, &diagnostics)
	if code != 0 {
		t.Fatalf("exit=%d: %s", code, &diagnostics)
	}
	decoder := json.NewDecoder(&out)
	for _, suffix := range []string{"/first", "/second"} {
		var result CheckResult
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.URL != server.URL+suffix {
			t.Fatalf("%+v", result)
		}
	}
}

func TestInvalidFileTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.txt")
	for _, content := range []string{"# no targets\n", "ftp://localhost\n", strings.Repeat("x", 70*1024)} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		var out, diagnostics bytes.Buffer
		if run([]string{"-file", path}, &out, &diagnostics) != 2 || out.Len() != 0 {
			t.Fatal(content[:min(len(content), 20)])
		}
	}
	var out, diagnostics bytes.Buffer
	if run([]string{"-file", path + ".missing"}, &out, &diagnostics) != 2 {
		t.Fatal("missing file accepted")
	}
}

func TestLatencyBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	for _, budget := range []string{"0", "10s", "1ns"} {
		var out, diagnostics bytes.Buffer
		code := run([]string{"-json", "-max-latency", budget, server.URL}, &out, &diagnostics)
		var result CheckResult
		if err := json.NewDecoder(&out).Decode(&result); err != nil {
			t.Fatal(err)
		}
		wantState, wantCode := "UP", 0
		if budget == "1ns" {
			wantState, wantCode = "SLOW", 1
		}
		if result.State != wantState || code != wantCode || result.Status != 204 {
			t.Fatalf("budget=%s result=%+v exit=%d", budget, result, code)
		}
	}
	var out, diagnostics bytes.Buffer
	if run([]string{"-max-latency", "-1s", server.URL}, &out, &diagnostics) != 2 {
		t.Fatal("negative latency budget accepted")
	}
}
