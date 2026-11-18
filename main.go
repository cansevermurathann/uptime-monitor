package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

type CheckResult struct {
	URL        string  `json:"url"`
	State      string  `json:"state"`
	Status     int     `json:"status"`
	DurationMS float64 `json:"duration_ms"`
	Error      string  `json:"error,omitempty"`
}

func run(args []string, out, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("uptime-monitor", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	jsonOutput := flags.Bool("json", false, "write one JSON object per target")
	timeout := flags.Duration("timeout", 5*time.Second, "maximum time per request")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() == 0 || *timeout <= 0 {
		fmt.Fprintln(diagnostics, "provide HTTP URLs and a positive -timeout")
		return 2
	}
	for _, target := range flags.Args() {
		u, err := url.Parse(target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
			fmt.Fprintln(diagnostics, "each target must be an HTTP(S) URL without credentials")
			return 2
		}
	}
	client := &http.Client{Timeout: *timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	defer client.CloseIdleConnections()
	exitCode := 0
	for _, target := range flags.Args() {
		started := time.Now()
		response, err := client.Get(target)
		elapsed := time.Since(started)

		result := CheckResult{URL: target, State: "UP", DurationMS: float64(elapsed) / float64(time.Millisecond)}
		if err != nil {
			result.State = "DOWN"
			result.Error = err.Error()
		} else {
			result.Status = response.StatusCode
			response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				result.State = "DOWN"
			}
		}
		if result.State == "DOWN" {
			exitCode = 1
		}
		var outputErr error
		if *jsonOutput {
			outputErr = json.NewEncoder(out).Encode(result)
		} else if err != nil {
			_, outputErr = fmt.Fprintf(out, "DOWN status=0 duration=%s url=%q error=%q\n", elapsed, target, err)
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

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
