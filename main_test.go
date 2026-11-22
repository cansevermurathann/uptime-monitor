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

func TestSensitiveURLPartsAreNotReported(t *testing.T) {
	received := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.URL.RawQuery
		w.WriteHeader(204)
	}))
	for _, format := range [][]string{{}, {"-json"}} {
		var out, diagnostics bytes.Buffer
		args := append(append([]string{}, format...), server.URL+"/health?token=private-value#private-fragment")
		if run(args, &out, &diagnostics) != 0 {
			t.Fatal(diagnostics.String())
		}
		if query := <-received; query != "token=private-value" {
			t.Fatal(query)
		}
		if strings.Contains(out.String(), "private") || strings.Contains(out.String(), "token") {
			t.Fatal(out.String())
		}
		if !strings.Contains(out.String(), server.URL+"/health") {
			t.Fatal(out.String())
		}
	}
	server.Close()
	for _, format := range [][]string{{}, {"-json"}} {
		var out, diagnostics bytes.Buffer
		args := append(append([]string{}, format...), server.URL+"?token=private-value#private-fragment")
		if run(args, &out, &diagnostics) != 1 {
			t.Fatal("expected connection failure")
		}
		if strings.Contains(out.String(), "private") || strings.Contains(out.String(), "token") {
			t.Fatal(out.String())
		}
	}
}

func TestExpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/redirect")
		w.WriteHeader(302)
	}))
	defer server.Close()
	for _, expected := range []string{"0", "204", "302"} {
		var out, diagnostics bytes.Buffer
		code := run([]string{"-json", "-expect-status", expected, server.URL}, &out, &diagnostics)
		var result CheckResult
		if err := json.NewDecoder(&out).Decode(&result); err != nil {
			t.Fatal(err)
		}
		wantCode, wantState := 1, "DOWN"
		if expected == "302" {
			wantCode, wantState = 0, "UP"
		}
		if code != wantCode || result.State != wantState || result.Status != 302 {
			t.Fatalf("expected=%s result=%+v exit=%d", expected, result, code)
		}
	}
	for _, value := range []string{"-1", "199", "600"} {
		var out, diagnostics bytes.Buffer
		if run([]string{"-expect-status", value, server.URL}, &out, &diagnostics) != 2 {
			t.Fatalf("invalid status accepted: %s", value)
		}
	}
}

func TestRequestMethods(t *testing.T) {
	for _, tc := range []struct {
		name, method, wantMethod string
		status, wantExit         int
		expect                   string
	}{
		{"default GET", "", "GET", 200, 0, ""},
		{"explicit GET", "GET", "GET", 200, 0, ""},
		{"HEAD success", "HEAD", "HEAD", 204, 0, ""},
		{"HEAD unsupported", "HEAD", "HEAD", 405, 1, ""},
		{"HEAD expected redirect", "HEAD", "HEAD", 302, 0, "302"},
		{"HEAD unexpected status", "HEAD", "HEAD", 200, 1, "204"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			methods := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods <- r.Method
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			args := []string{"-json"}
			if tc.method != "" {
				args = append(args, "-method", tc.method)
			}
			if tc.expect != "" {
				args = append(args, "-expect-status", tc.expect)
			}
			args = append(args, server.URL)
			var out, diagnostics bytes.Buffer
			code := run(args, &out, &diagnostics)
			if code != tc.wantExit {
				t.Fatalf("exit=%d diagnostic=%s", code, &diagnostics)
			}
			select {
			case method := <-methods:
				if method != tc.wantMethod {
					t.Fatalf("method=%s want=%s", method, tc.wantMethod)
				}
			default:
				t.Fatal("no request received")
			}
			if len(methods) != 0 {
				t.Fatal("unexpected redirect or fallback request")
			}
			var result CheckResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.status || result.DurationMS < 0 {
				t.Fatalf("unexpected result: %+v", result)
			}
		})
	}
}

func TestInvalidMethodSendsNoRequests(t *testing.T) {
	requests := make(chan struct{}, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
	}))
	defer server.Close()
	for _, method := range []string{"", "head", "POST", "DELETE", "GET\r\n"} {
		var out, diagnostics bytes.Buffer
		if code := run([]string{"-method", method, server.URL}, &out, &diagnostics); code != 2 ||
			out.Len() != 0 || !strings.Contains(diagnostics.String(), "method must be GET or HEAD") {
			t.Fatalf("method=%q exit=%d out=%s diagnostics=%s", method, code, &out, &diagnostics)
		}
	}
	if len(requests) != 0 {
		t.Fatal("invalid method sent a request")
	}
}

func TestHeadTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	if code := run([]string{"-method", "HEAD", "-timeout", "50ms", server.URL}, &out, &diagnostics); code != 1 ||
		!strings.Contains(out.String(), "request timed out") {
		t.Fatalf("exit=%d out=%s diagnostics=%s", code, &out, &diagnostics)
	}
}

func TestBatchDeadlineCancelsActiveAndQueuedChecks(t *testing.T) {
	for _, workers := range []string{"1", "2"} {
		t.Run("workers="+workers, func(t *testing.T) {
			requests := make(chan string, 10)
			canceled := make(chan struct{}, 10)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.URL.Path
				if r.URL.Path == "/fast" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				<-r.Context().Done()
				canceled <- struct{}{}
			}))
			defer server.Close()
			args := []string{"-deadline", "200ms", "-timeout", "5s", "-concurrency", workers, "-json",
				server.URL + "/fast", server.URL + "/slow1", server.URL + "/slow2",
				server.URL + "/slow3", server.URL + "/slow4"}
			var out, diagnostics bytes.Buffer
			started := time.Now()
			code := run(args, &out, &diagnostics)
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("batch took %s despite shared deadline", elapsed)
			}
			if code != 1 {
				t.Fatalf("exit=%d diagnostic=%s", code, &diagnostics)
			}
			decoder := json.NewDecoder(&out)
			for i, target := range args[len(args)-5:] {
				var result CheckResult
				if err := decoder.Decode(&result); err != nil {
					t.Fatal(err)
				}
				if result.URL != target {
					t.Fatalf("out of order: %+v", result)
				}
				if i == 0 {
					if result.State != "UP" || result.Status != 204 {
						t.Fatalf("lost successful result: %+v", result)
					}
				} else if result.State != "DOWN" || result.Status != 0 || result.Error != "request timed out" {
					t.Fatalf("unfinished check: %+v", result)
				}
			}
			var extra CheckResult
			if err := decoder.Decode(&extra); err != io.EOF {
				t.Fatalf("extra output: %v", err)
			}
			maxRequests := 2
			if workers == "2" {
				maxRequests = 3
			}
			if len(requests) < 2 || len(requests) > maxRequests {
				t.Fatalf("queued targets reached server: %d requests", len(requests))
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("server did not observe cancellation")
			}
		})
	}
}

func TestRequestTimeoutWinsBeforeBatchDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	code := run([]string{"-deadline", "5s", "-timeout", "50ms", server.URL + "/slow", server.URL + "/fast"}, &out, &diagnostics)
	if code != 1 || !strings.Contains(out.String(), "request timed out") || !strings.Contains(out.String(), "UP status=204") {
		t.Fatalf("exit=%d out=%s diagnostic=%s", code, &out, &diagnostics)
	}
}

func TestNegativeDeadlineSendsNoRequests(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests <- struct{}{} }))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	if code := run([]string{"-deadline", "-1s", server.URL}, &out, &diagnostics); code != 2 ||
		out.Len() != 0 || !strings.Contains(diagnostics.String(), "deadline must be non-negative") || len(requests) != 0 {
		t.Fatalf("exit=%d out=%s diagnostic=%s", code, &out, &diagnostics)
	}
}
