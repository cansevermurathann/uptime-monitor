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

URLs are positional arguments and flags precede them. Requests run sequentially by default.
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


## Check services concurrently

Use `-concurrency 4` to run up to four requests at once. The allowed range is
1–32. Results are buffered and printed in input order after all checks finish.
Each request keeps its own timeout, which starts when that request begins.

```sh
./uptime-monitor -json -concurrency 4 -timeout 2s http://localhost:8000 http://localhost:8001
```

## Reuse a target list

Create a UTF-8 text file with one URL per line. Blank lines and lines beginning
with `#` are ignored, and surrounding whitespace is trimmed. Both Windows
and Unix line endings are supported.

```text
# Local services
http://127.0.0.1:8000/
http://127.0.0.1:8001/
```

```sh
./uptime-monitor -file targets.txt -concurrency 4 -json
```

File targets precede any positional URLs. All targets are validated before
requests start. Missing files, unreadable files, lines above the scanner's
64 KiB limit, and invalid URLs return exit code 2.

## Continuous integration

The [Go checks workflow](https://github.com/cansevermurathann/uptime-monitor/actions/workflows/tests.yml)
checks formatting, runs local-server tests with the race detector, analyzes
code with `go vet`, and builds the CLI. It runs on Linux with Go 1.24 and 1.27
for pushes and pull requests targeting `main`, and can be started manually.

## Enforce a response-time budget

```sh
./uptime-monitor -max-latency 500ms -timeout 2s -json http://localhost:8000
```

A successful HTTP 2xx response taking more than the budget is reported as
`SLOW` with its actual status code and exit code 1. The budget measures time
to response headers. `-timeout` still cancels requests that take too long.
The default budget is 0 (disabled). Negative budgets are configuration errors.

## URL reporting

Reports omit URL query strings and fragments, which can contain access tokens.
The HTTP request still uses the configured query string. Request errors use
generic messages (`request failed` or `request timed out`) so Go's error
strings cannot repeat the full request URL. URL paths and hostnames remain
visible in reports.

## Require an exact status

Use `-expect-status 204` when a health endpoint must return a specific HTTP
code. The default is 0, which accepts any 2xx response. Explicit expectations
accept codes from 200 through 599, including redirects without following them.

```sh
./uptime-monitor -expect-status 204 -json http://localhost:8000/health
```

A different response is DOWN with exit code 1. Connection errors remain DOWN
regardless of the expected code. The latency budget also applies to responses
that match an explicit expected status.

## Check with HEAD

Use HEAD for endpoints that support availability checks without transferring
a response body. GET remains the default; the selected method applies to all
targets in the invocation.

```sh
./uptime-monitor -method HEAD -json http://127.0.0.1:8000/
./uptime-monitor -method HEAD -expect-status 204 -timeout 2s http://localhost:8001/health
```

The local Python server in the demo supports HEAD. Some services reject HEAD
with 405; that response is reported as DOWN under the default status policy.
There is no automatic fallback to GET. Only uppercase GET and HEAD are accepted;
other methods return exit code 2 before any requests are sent. Timeout, exact
status expectations, latency budgets, redirects, and output formats keep the
same behavior for both methods.
