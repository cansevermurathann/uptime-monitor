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
following them. Connection failures before headers report status 0. The default
timeout is five seconds per request. Unless body checking is enabled, the response
body is closed after headers arrive. Reported latency measures time to headers. Exit codes are 0 for all
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
Body-read failures retain the HTTP status already received.
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
and Unix line endings are supported. An optional UTF-8 BOM at the start
of the input is ignored.

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

## Bound a whole batch of checks

Use a shared deadline when a CI step or scheduled check must finish its network
work within a fixed budget:

```sh
./uptime-monitor -deadline 10s -timeout 3s -concurrency 4 -file targets.txt -json
```

The deadline starts after target loading and validation, immediately before the
workers start. It includes time waiting in the worker queue. At expiry, active
requests are canceled and queued checks return without sending new requests.
Each unfinished target is reported as DOWN with status 0 and
`request timed out`; results already obtained are preserved in input order.
Any failed check still returns exit code 1. A deadline is a local budget
failure and does not prove that every affected service is unavailable.

The per-request timeout remains in effect: whichever limit expires first wins.
The default deadline is 0 (disabled); negative values return exit code 2 before
sending requests. Loading files and writing results are outside this network
budget, and normal cancellation/scheduling overhead may add a small delay.

## Trust an internal HTTPS certificate authority

For services signed by a private development or company CA, provide the public
CA certificate bundle as a PEM file:

```sh
./uptime-monitor -ca-file company-ca.pem -timeout 2s -json https://localhost:8443/health
```

The bundle extends the operating system's trusted roots for this invocation.
It applies to all HTTPS targets in the invocation. Normal certificate chain,
expiration, and hostname verification remain enabled. A missing file, a file
without any parseable PEM certificates, or failure to load the system roots
returns exit code 2 before any requests start. TLS verification failures are
reported as DOWN with status 0 and exit code 1.

Only include public certificates in this file; private keys are unnecessary.
Trust only CA certificates you intend to authorize. The tool does not modify
the system trust store or the process-wide default HTTP transport. Without
`-ca-file`, the normal system trust settings apply.

Tests generate temporary certificates and local TLS servers to cover trusted
and untrusted issuers, incorrect hostnames, and expired certificates. No
certificate keys or external HTTPS services are needed to run the tests.

## Diagnose failed requests

Failed requests include a stable `error_kind` field in JSON and
`error_kind=...` in text output:

| Kind | Meaning |
| --- | --- |
| `batch_deadline` | The shared network budget expired for this request |
| `timeout` | An individual request or network operation timed out |
| `dns` | A DNS lookup failed without a recognized timeout |
| `tls_verification` | The server certificate could not be verified |
| `network` | Another network operation failed, such as connection refusal |
| `request` | Another request failure not covered above |

Timeout classification takes precedence over DNS or other network errors.
A shared budget is identified only when the request error wraps
`context.DeadlineExceeded` and the batch context has expired. Categories describe
the error observed by Go; they are not a complete diagnosis of the remote service.

```sh
./uptime-monitor -json -timeout 2s -deadline 5s https://localhost:8443/health
```

For example, an untrusted local HTTPS certificate produces:

```json
{"url":"https://localhost:8443/health","state":"DOWN","status":0,"duration_ms":1.2,"error":"request failed","error_kind":"tls_verification"}
```

Duration varies by request. Completed HTTP checks (including failed status
checks and SLOW results) omit `error_kind`. Body-read failures include it. Exit
codes and the existing sanitized `error` messages are unchanged. Categories
are derived from typed errors without printing underlying error text, so
request URLs containing query tokens are not exposed by diagnostics.

## Warn before an HTTPS certificate expires

Use a renewal window to detect a certificate approaching expiration while
the service is still reachable:

```sh
./uptime-monitor -cert-expiry 168h -json https://localhost:8443/health
./uptime-monitor -ca-file company-ca.pem -cert-expiry 720h https://localhost:8443/health
```

A check that would otherwise be UP becomes `EXPIRING` with exit code 1 when
the server's leaf certificate expires at or before the current time plus the
window. HTTP status and response time are retained. The default window is
0 (disabled). Use hours for days (`168h` is seven days); negative values
return exit code 2 before requests start.

Responses received over HTTPS include `tls_expires_at`, a UTC RFC 3339 timestamp,
in JSON and text output, even when the window is disabled. Plain HTTP and
failed TLS handshakes omit this field. Plain HTTP checks are unaffected by the
window. Existing DOWN and SLOW outcomes take precedence over EXPIRING.

Normal TLS verification remains enabled. An already expired or untrusted
certificate still fails the handshake and reports DOWN with
`error_kind=tls_verification`. This feature checks the leaf certificate only;
it does not warn about intermediate/root expiry or implement revocation checks.
Tests use generated local certificates, with no external services or checked-in keys.

## Verify a health response body

A reachable service can return a login page or a degraded health message with
HTTP 200. Require a case-sensitive literal substring to catch this:

```sh
./uptime-monitor -expect-body '"status":"healthy"' -timeout 2s -json http://localhost:8000/health
```

This is a literal byte substring check, not JSON parsing or a regular expression.
The check reads at most the first 64 KiB of the decoded response body. A match
must be fully contained in that prefix. It waits for EOF, the prefix limit,
or a read error; finding a substring early does not stop the read. An empty
expectation disables body checks. Expectations longer than 64 KiB or using
a non-empty expectation with HEAD return exit code 2 before any requests.

JSON includes `body_matches` when the assertion was evaluated. A check that
passes the status policy but lacks the substring becomes `MISMATCH` with exit
code 1. A failed HTTP status remains DOWN. MISMATCH takes precedence over
latency and certificate-expiry warnings. Neither response contents nor the
expected substring is included in reports.

Both the per-request timeout and shared deadline apply while reading the body.
Read failures report DOWN with a sanitized error and `error_kind`, preserving
the received HTTP status; `body_matches` is omitted because the assertion was
not completed. `duration_ms` still measures time to response headers, while
the invocation may additionally spend time reading the bounded body.

For a local demo, put `{"status":"healthy"}` in `health.json` beside your demo
Python server's files, then run:

```sh
./uptime-monitor -expect-body '"status":"healthy"' http://127.0.0.1:8000/health.json
```

## Read a generated target list from stdin

Use `-file -` to pipe a finite list from another command without a temporary file:

```sh
cat targets.txt | ./uptime-monitor -file - -concurrency 4 -timeout 2s -json
```

PowerShell:

```powershell
Get-Content .\targets.txt | .\uptime-monitor.exe -file - -concurrency 4 -json
```

Stdin uses the same format as target files: one URL per line, optional blank
lines and full-line `#` comments, whitespace trimming, LF/CRLF endings, and an
optional UTF-8 BOM at the very beginning. UTF-16 input is not supported.
An unterminated final line is accepted. The scanner's 64 KiB line limit
also applies. A real file named `-` can be read as `-file ./-`.

Input targets precede positional URLs and retain their order in output.
The entire input is read to EOF and all URLs are validated before any network
requests start. An input error, an oversized line, or an invalid URL returns
exit code 2 without sending requests; empty input requires positional targets.
Stdin is only read when explicitly selected with `-file -` and is not closed
by the application.

The producer must finish its output and close the pipe. Target loading is
outside the `-deadline` network budget, so this option does not impose a timeout
on waiting for stdin. This is a single batch, not a continuous stream monitor.
