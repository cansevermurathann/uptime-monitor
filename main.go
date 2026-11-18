package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

func run(args []string, out, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("uptime-monitor", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
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
		if err != nil {
			fmt.Fprintf(out, "DOWN status=0 duration=%s url=%q error=%q\n", elapsed, target, err)
			exitCode = 1
			continue
		}
		response.Body.Close()
		state := "UP"
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			state = "DOWN"
			exitCode = 1
		}
		fmt.Fprintf(out, "%s status=%d duration=%s url=%q\n", state, response.StatusCode, elapsed, target)
	}
	return exitCode
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
