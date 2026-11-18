# Uptime Monitor

A Go command-line tool for checking HTTP service availability once. It reports
the HTTP status and time to response headers for each configured URL.

## Install and run

Install Go 1.24 or newer, clone this repository, and build the executable.

```sh
go build -o uptime-monitor .
./uptime-monitor -timeout 2s http://localhost:8000
go test -race ./...
go vet ./...
```

On Windows, build with `go build -o uptime-monitor.exe .` and run
`.\uptime-monitor.exe -timeout 2s http://localhost:8000`.

For a local demo, start a file server in a second terminal with
`python -m http.server 8000 --bind 127.0.0.1`. Check both a valid page and
a missing page:

```sh
./uptime-monitor http://127.0.0.1:8000/ http://127.0.0.1:8000/missing-page
```

URLs are positional arguments and flags precede them. Requests run sequentially.
Only HTTP 2xx responses count as UP. Redirects are reported as DOWN without
following them. Timeouts and connection errors report status 0. The default
timeout is five seconds per request. The response body is closed after headers
arrive, so the tool measures reachability to headers. Exit codes are 0 for all
checks passing, 1 for any failed check, and 2 for invalid arguments.

Tests use local HTTP servers for successful responses, redirects, server
errors, timeout cancellation, and continued checking after a failure.

## Machine-readable output

Add `-json` to emit one JSON object per target, in input order:

```sh
./uptime-monitor -json -timeout 2s http://localhost:8000
```

Each object contains `url`, `state`, `status`, and numeric `duration_ms`.
Connection failures additionally contain an `error` string and status 0.
JSON output uses the same exit codes as text output. Output write failures
return exit code 2 and write a diagnostic to stderr.
