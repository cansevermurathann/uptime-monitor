package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type CheckResult struct {
	URL          string     `json:"url"`
	State        string     `json:"state"`
	Status       int        `json:"status"`
	DurationMS   float64    `json:"duration_ms"`
	Error        string     `json:"error,omitempty"`
	ErrorKind    string     `json:"error_kind,omitempty"`
	TLSExpiresAt *time.Time `json:"tls_expires_at,omitempty"`
	BodyMatches  *bool      `json:"body_matches,omitempty"`
}

func run(args []string, out, diagnostics io.Writer) int {
	return runWithInput(args, os.Stdin, out, diagnostics)
}

func runWithInput(args []string, input io.Reader, out, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("uptime-monitor", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	expectedBody := flags.String("expect-body", "", "require a literal substring in the first 64 KiB of a GET response")
	certExpiry := flags.Duration("cert-expiry", 0, "fail HTTPS checks when the leaf certificate expires within this duration (0 disables)")
	caFile := flags.String("ca-file", "", "PEM certificates to trust in addition to system roots")
	deadline := flags.Duration("deadline", 0, "maximum time for the entire request phase (0 disables)")
	method := flags.String("method", http.MethodGet, "HTTP request method (GET or HEAD)")
	expectedStatus := flags.Int("expect-status", 0, "require an exact final HTTP status (0 accepts any 2xx)")
	file := flags.String("file", "", "read one HTTP URL per line (use - for stdin)")
	workers := flags.Int("concurrency", 1, "maximum simultaneous requests (1-32)")
	jsonOutput := flags.Bool("json", false, "write one JSON object per target")
	maxLatency := flags.Duration("max-latency", 0, "fail successful checks above this response-header latency (0 disables)")
	timeout := flags.Duration("timeout", 5*time.Second, "maximum time per request")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *expectedBody != "" && (*method != http.MethodGet || len(*expectedBody) > 64*1024) {
		fmt.Fprintln(diagnostics, "expect-body requires GET and a substring of at most 64 KiB")
		return 2
	}
	if *certExpiry < 0 {
		fmt.Fprintln(diagnostics, "cert-expiry must be non-negative")
		return 2
	}
	if *deadline < 0 {
		fmt.Fprintln(diagnostics, "deadline must be non-negative")
		return 2
	}
	if *method != http.MethodGet && *method != http.MethodHead {
		fmt.Fprintln(diagnostics, "method must be GET or HEAD")
		return 2
	}
	if *expectedStatus != 0 && (*expectedStatus < 200 || *expectedStatus > 599) {
		fmt.Fprintln(diagnostics, "expect-status must be 0 or an HTTP status from 200 to 599")
		return 2
	}

	targets := flags.Args()
	if *file != "" {
		var loaded []string
		var err error
		if *file == "-" {
			loaded, err = readTargets(input)
		} else {
			loaded, err = loadTargets(*file)
		}
		if err != nil {
			fmt.Fprintln(diagnostics, "read targets:", err)
			return 2
		}
		targets = append(loaded, targets...)
	}
	if len(targets) == 0 || *timeout <= 0 || *workers < 1 || *workers > 32 || *maxLatency < 0 {
		fmt.Fprintln(diagnostics, "provide HTTP URLs, a positive -timeout, -concurrency from 1 to 32, and non-negative -max-latency")
		return 2
	}
	for _, target := range targets {
		u, err := url.Parse(target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
			fmt.Fprintln(diagnostics, "each target must be an HTTP(S) URL without credentials")
			return 2
		}
	}
	transport, err := newTransport(*caFile)
	if err != nil {
		fmt.Fprintln(diagnostics, "configure TLS:", err)
		return 2
	}
	client := &http.Client{Transport: transport, Timeout: *timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	defer client.CloseIdleConnections()
	ctx := context.Background()
	if *deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *deadline)
		defer cancel()
	}
	results := make([]CheckResult, len(targets))
	jobs := make(chan int)
	var group sync.WaitGroup
	for worker := 0; worker < min(*workers, len(targets)); worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				results[index] = check(ctx, client, targets[index], *method, *expectedBody)
				if *expectedStatus != 0 && results[index].Error == "" {
					results[index].State = "DOWN"
					if results[index].Status == *expectedStatus {
						results[index].State = "UP"
					}
				}
				if results[index].State == "UP" && results[index].BodyMatches != nil && !*results[index].BodyMatches {
					results[index].State = "MISMATCH"
				}
				if results[index].State == "UP" && *maxLatency > 0 && results[index].DurationMS > float64(*maxLatency)/float64(time.Millisecond) {
					results[index].State = "SLOW"
				}
				if results[index].State == "UP" && *certExpiry > 0 && results[index].TLSExpiresAt != nil &&
					!results[index].TLSExpiresAt.After(time.Now().Add(*certExpiry)) {
					results[index].State = "EXPIRING"
				}
			}
		}()
	}
	for index := range targets {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	exitCode := 0
	for _, result := range results {
		target := result.URL
		elapsed := time.Duration(result.DurationMS * float64(time.Millisecond))
		if result.State != "UP" {
			exitCode = 1
		}
		var outputErr error
		if *jsonOutput {
			outputErr = json.NewEncoder(out).Encode(result)
		} else if result.Error != "" {
			_, outputErr = fmt.Fprintf(out, "DOWN status=%d duration=%s url=%q error=%q error_kind=%s\n", result.Status, elapsed, target, result.Error, result.ErrorKind)
		} else if result.TLSExpiresAt != nil {
			_, outputErr = fmt.Fprintf(out, "%s status=%d duration=%s url=%q tls_expires_at=%s\n",
				result.State, result.Status, elapsed, target, result.TLSExpiresAt.Format(time.RFC3339))
		} else {
			_, outputErr = fmt.Fprintf(out, "%s status=%d duration=%s url=%q\n", result.State, result.Status, elapsed, target)
		}
		if outputErr != nil {
			fmt.Fprintln(diagnostics, "write results:", outputErr)
			return 2
		}

	}
	return exitCode
}

// Each invocation owns its transport and its additional trust roots.
func newTransport(caFile string) (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caFile == "" {
		return transport, nil
	}
	data, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system roots: %w", err)
	}
	if !roots.AppendCertsFromPEM(data) {
		return nil, errors.New("CA file must contain at least one PEM certificate")
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	return transport, nil
}

func loadTargets(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readTargets(file)
}

func readTargets(input io.Reader) ([]string, error) {
	var targets []string
	scanner := bufio.NewScanner(input)
	firstLine := true
	for scanner.Scan() {
		line := scanner.Text()
		if firstLine {
			line = strings.TrimPrefix(line, "\uFEFF")
			firstLine = false
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		targets = append(targets, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return targets, nil
}

func check(ctx context.Context, client *http.Client, target, method, expectedBody string) CheckResult {
	started := time.Now()
	request, err := http.NewRequestWithContext(ctx, method, target, nil)
	var response *http.Response
	if err == nil {
		response, err = client.Do(request)
	}
	elapsed := time.Since(started)

	displayURL, _ := url.Parse(target)
	displayURL.RawQuery = ""
	displayURL.ForceQuery = false
	displayURL.Fragment = ""
	displayURL.RawFragment = ""
	result := CheckResult{URL: displayURL.String(), State: "UP", DurationMS: float64(elapsed) / float64(time.Millisecond)}
	if err != nil {
		result.State = "DOWN"
		result.Error = "request failed"
		result.ErrorKind = requestErrorKind(ctx, err)
		if result.ErrorKind == "timeout" || result.ErrorKind == "batch_deadline" {
			result.Error = "request timed out"
		}
	} else {
		result.Status = response.StatusCode
		if response.TLS != nil && len(response.TLS.PeerCertificates) > 0 {
			expires := response.TLS.PeerCertificates[0].NotAfter.UTC()
			result.TLSExpiresAt = &expires
		}
		defer response.Body.Close()
		if expectedBody != "" {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 64*1024))
			if readErr != nil {
				result.State = "DOWN"
				result.Error = "response body read failed"
				result.ErrorKind = requestErrorKind(ctx, readErr)
				if result.ErrorKind == "timeout" || result.ErrorKind == "batch_deadline" {
					result.Error = "response body read timed out"
				}
				return result
			}
			matches := strings.Contains(string(body), expectedBody)
			result.BodyMatches = &matches
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			result.State = "DOWN"
		}
	}

	return result
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
