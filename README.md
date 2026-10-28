# herakles

[![ci](https://github.com/raulnid/herakles/actions/workflows/ci.yml/badge.svg)](https://github.com/raulnid/herakles/actions/workflows/ci.yml)

A synthetic probe for multi-step user journeys, built to run inside Kubernetes
and report to Prometheus.

You describe a journey in YAML, the way a real client walks it: log in with an
API key, upload a file, download it and check it is the same, edit it, delete
it, log out. herakles repeats it every interval against each environment and
exposes how every step went as metrics.

```
              every interval, per target, in parallel
  ┌─────────────────────────────────────────────────────────────┐
  │  login ──► projects ─────────────────────► logout           │
  │  api key   upload zip, download + sha256,  (finalizer:      │
  │  → token   rename, delete, check it's gone  always runs)    │
  └─────────────────────────────────────────────────────────────┘
                      │ one result per step
                      ▼
           /metrics ◄── Prometheus ── alerts
```

## Why

A blackbox probe answers "does the endpoint respond?". A journey probe answers
the question your users care about: **can a client log in and do its work right
now?** It catches what a ping cannot: a login that returns 200 with a token
that does not work, storage that accepts an upload and serves back corrupted
bytes, a delete that answers 204 and deletes nothing.

herakles is not a load generator like k6 or Artillery. It runs one journey at
a time per environment and measures it precisely: per-step latency, HTTP
phases (DNS, connect, TLS, time to first byte, transfer), and upload and
download rates. It is designed as a long-running process with native metrics,
not as a CLI tool that prints a report and exits.

## Quickstart

Requires Go 1.26. From the repository root, start the demo API and run one
cycle of the [example journey](examples/herakles.yaml):

```sh
go run ./examples/eurystheus &
go run ./cmd/herakles run -once -config examples/herakles.yaml
```

```
22:14:34.865 INFO  prod     projects       2/6   upload                 | -- skipped: mutating step on a safe target
22:14:34.865 INFO  prod     projects       3/6   download               | -- skipped: needs "project_id", which was not set
22:14:34.866 INFO  dev      projects       2/6   upload                 | >> Upload a 5 MiB zip of random content  needs: base_url, token
22:14:34.881 INFO  dev      projects       2/6   upload                 | ok 16ms -> project_id, upload  up 471.4 MB/s
22:14:34.881 INFO  dev      projects       3/6   download               | >> The stored file matches the upload byte for byte  needs: base_url, project_id, token, upload
22:14:34.885 INFO  dev      projects       3/6   download               | ok 4ms  down 1.4 GB/s
...
22:14:34.887 INFO  dev      (cycle)                                     | == ok in 25ms
```

The exit code is 0 when every required step passed and 1 otherwise. Without
`-once`, herakles keeps running and serves metrics on `:9090/metrics`.

To deploy it to a local kind or k3d cluster, see
[examples/kubernetes](examples/kubernetes).

## Concepts

| Term | Meaning |
|---|---|
| **target** | An environment to probe (dev, prod...), with its own variables. Targets run in parallel and never wait for each other: a slow or failing target does not delay the others. Every metric carries a `target` label. |
| **labor** | A scenario: an ordered list of steps. Labors run in order. When a required step fails, the labor stops and records which step failed. The remaining labors of the cycle are skipped. |
| **step** | One action, done by a module (`http` for now). It declares what it does, reads variables set earlier and sets new ones. `optional: true` records a failure without stopping the labor. |
| **finally** | Cleanup steps of a labor that always run, like a `defer`. Example: delete what the labor created, even if a step in between failed. A finally step whose variables were never set is skipped, because there is nothing to clean up. |
| **finalizer** | A labor marked `finalizer: true`. It runs at the end of every cycle, even after a failure. Use it for logout. |
| **cycle** | One run of every labor of a target, every `interval`. Each cycle starts with empty variables: no token or id from a previous cycle is ever reused. File and env variables are read again every cycle, so a rotated secret is picked up on its own. |
| **safe / mutating** | Steps that write are marked `mutating: true`. On a target with `safe: true` they are skipped, and so are the steps that need their outputs. The same YAML probes dev read-write and prod read-only. |

## Configuration

One YAML file, which can be mounted from a ConfigMap. A JSON Schema at
[`schema/herakles.schema.json`](schema/herakles.schema.json) gives your editor
completion and validation. With the YAML extension in VS Code, add this first
line:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/raulnid/herakles/main/schema/herakles.schema.json
```

`herakles validate -config FILE` checks a config without running anything. It
checks the structure, the module configs and template syntax, and that every
variable a step reads is declared or set by an earlier step, for every target.
`run` performs the same checks before it starts.

### Global settings

| Key | Default | |
|---|---|---|
| `interval` | `60s` | Time between the start of two cycles of a target. A cycle that overruns starts the next one right away, never two at once. |
| `step_timeout` | `10s` | Time limit for each step. |
| `transfer_timeout` | `120s` | Time limit for steps that upload a payload or verify a download (`assert.sha256` or `assert.size`). |
| `listen` | `:9090` | Address of `/metrics` and `/health`. |
| `user_agent` | `herakles/<version> (synthetic probe)` | Sent on every request, so the traffic can be filtered out of your stats. |

### Variables

```yaml
vars:                                   # shared by every target
  api_key: {file: /etc/herakles/secrets/api-key}
  region:  {env: REGION}
targets:
  - name: dev
    vars:                               # override global vars of the same name
      base_url: {value: "https://dev.example.com"}
```

Each variable has exactly one source: `value` (a literal), `file` or `env`.
**Secrets never go in the YAML.** Read them from a file (a mounted Secret) or
from an environment variable. File variables are always treated as secret;
mark others with `secret: true`.

Templates use Go's [`text/template`](https://pkg.go.dev/text/template) and work
in every field of a step config:

| Expression | Value |
|---|---|
| `{{ .vars.token }}` | A variable, declared or set by an earlier step. |
| `{{ .vars.upload.sha256 }}` | A field of a structured variable, such as a payload. |
| `{{ .target }}` | The target name. |
| `{{ .cycle.id }}` | A random id per cycle, for unique resource names. |
| `{{ .vars.id \| pathescape }}` | The value escaped for a URL path segment. |
| `{{ .vars.q \| urlquery }}` | The value escaped for a query string. |

Values captured from responses are inserted as they are. Escape them when they
go into a URL, so an id like `a/b?x` cannot change the path or the query:
`/projects/{{ .vars.project_id | pathescape }}`.

How templates render:

- A field that is exactly one variable keeps that variable's type. `size: "{{ .vars.upload.size }}"` stays a number, and `{"api_key": "{{ .vars.key }}"}` stays a string even when the key looks like `0123`.
- A field that mixes text and templates always renders to a string.
- Reading an undefined variable fails the step. A finally or finalizer step is skipped instead.

### Targets

```yaml
targets:
  - name: prod
    safe: true                          # skip mutating steps
    vars: {base_url: {value: "https://api.example.com"}}
    tls:
      ca_file: /etc/ssl/internal-ca.pem # added to the system roots, read every cycle
      insecure_skip_verify: false
```

### Labors and steps

```yaml
labors:
  - name: projects
    steps:
      - name: upload                    # the step label; unique within the labor
        description: Upload a 5 MiB zip # shown in the log when the step starts
        mutating: true                  # skipped on safe targets
        optional: false                 # true: a failure does not stop the labor
        http: {...}                     # exactly one module key
    finally:
      - name: cleanup
        http: {...}
  - name: logout
    finalizer: true
    steps: [...]
```

### The `http` module

```yaml
http:
  method: POST                          # default GET
  url: "{{ .vars.base_url }}/projects"
  headers: {Authorization: "Bearer {{ .vars.token }}"}
  body:                                 # at most one of:
    json: {name: "synthetic-{{ .cycle.id }}"}
    text: "plain text"
    payload: {kind: zip, size: 5MiB, as: upload}     # generated file as the raw body
    multipart:
      name: synthetic                                # form field
      file: {payload: {kind: zip, size: 5MiB, as: upload}}
      notes: {value: "hello", filename: notes.txt, content_type: text/plain}
  capture:                              # set variables from the response
    token:   {jsonpath: $.token, secret: true}
    etag:    {header: ETag}
    code:    {status: true}
  assert:
    status: [200, 201]                  # default: any 2xx
    jsonpath: {$.user.id: 7, $.name: "synthetic-{{ .cycle.id }}"}
    sha256: "{{ .vars.upload.sha256 }}" # expected SHA-256 of the response body
    sha256_header: Content-Digest       # ...and/or the digest the server sends with it
    size: "{{ .vars.upload.size }}"     # length of the response body
```

- **Expected failures** use `assert.status`, which replaces the 2xx default. `assert: {status: [401, 403]}` passes only if the request is rejected. That covers "without a token the API refuses" and "a closed token no longer works".
- **JSONPath** uses the full [JSONPath](https://goessner.net/articles/JsonPath/) syntax via [ojg](https://github.com/ohler55/ojg). A capture takes the first match. Responses used by `jsonpath` are parsed up to 1 MiB.
- **Downloads** are streamed: the body is hashed and counted as it arrives and never kept whole in memory. The hash is computed only when `sha256` or `sha256_header` is set. See [Verifying downloads](#verifying-downloads).
- **Error messages** include at most the first 200 bytes of the response body, with every secret masked.
- **Redirects** are followed, up to 10. On a redirect to another host, every header that carries a secret is dropped, not just `Authorization` and `Cookie` as Go does by default. A presigned storage URL gets the request; your API key does not.
- **Sessions**: a cycle uses one HTTP client per target, with keep-alive and a cookie jar, like a real client. The connection is reused within the cycle and dropped at its end.

### Payloads

Synthetic files are generated in memory and streamed, without touching disk.
They are hashed (SHA-256) as they are sent. With `as: upload`, the step sets
`{{ .vars.upload.sha256 }}` and `{{ .vars.upload.size }}`, so a later step can
check the download against the upload.

| Kind | Content | Content-Type |
|---|---|---|
| `random` | Random bytes, exactly `size` long | `application/octet-stream` |
| `zip` | A valid zip holding one stored (uncompressed) random file of `size` bytes | `application/zip` |
| `png` | A valid RGB PNG with about `size` bytes of random pixels | `image/png` |

The content is random and incompressible, so compression along the path cannot
make a transfer look faster than it is. Sizes accept `1024`, `10KB`, `5MiB` and
so on, up to 4 GiB.

### Verifying downloads

herakles always hashes the downloaded body (SHA-256, streamed). What it
compares the hash against is up to you. Combine sources: every one you set
must match.

| Expected value comes from | Config | Catches |
|---|---|---|
| **What herakles uploaded.** The payload's hash, computed while it was sent. | `sha256: "{{ .vars.upload.sha256 }}"` | Anything that changed the bytes between upload and download: storage, proxies, the API. This is the strongest check. |
| **The upload response.** The server tells you what it stored. | capture `{jsonpath: $.sha256}` on the upload step, then `sha256: "{{ .vars.stored_sha }}"` | A server that stored something other than what it acknowledges. Compare `{{ .vars.upload.sha256 }}` with it in an `assert.jsonpath` to check the upload itself. |
| **A metadata endpoint.** A separate call, such as `GET /files/{id}`, returns the checksum. | capture it in that step, then `sha256: "{{ .vars.meta_sha }}"` | Metadata and content drifting apart. |
| **The download response itself.** A header sent with the file. | `sha256_header: Content-Digest` | Corruption in transit. It is weaker on its own: a server that stores bad bytes and hashes them will agree with itself. |

`sha256_header` understands the common formats (values below are the digest of `hello world`):

| Header | Example value |
|---|---|
| `Content-Digest` / `Repr-Digest` ([RFC 9530](https://www.rfc-editor.org/rfc/rfc9530)) | `sha-256=:uU0nuZNNPgilLlLX2n2r+sSE7+N6U4DukIj3rOLvzek=:` (other algorithms in the list are ignored) |
| `Digest` ([RFC 3230](https://www.rfc-editor.org/rfc/rfc3230), legacy) | `SHA-256=uU0nuZNNPgilLlLX2n2r+sSE7+N6U4DukIj3rOLvzek=` |
| `x-amz-checksum-sha256` (S3) and other base64 headers | `uU0nuZNNPgilLlLX2n2r+sSE7+N6U4DukIj3rOLvzek=` |
| `X-Checksum-Sha256` and other hex headers | `b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9` |

Points to keep in mind:

- **For zip payloads, the hash is of the whole archive** as sent. That is what a server stores and returns, so it matches a plain file download.
- **Compression.** A server that compresses the response on the fly (`Content-Encoding`) changes the bytes: Go decompresses `gzip` transparently, so herakles hashes the decoded content, as `Repr-Digest` describes. A `Content-Digest` of the compressed bytes would not match then. Payloads are incompressible, so servers rarely compress them.
- **Size alone** (`assert.size`) is cheap but only detects truncation.

The example journey uses both `sha256` against the upload and
`sha256_header: Content-Digest`, which the eurystheus mock sends.

## Command line

Every [release](https://github.com/raulnid/herakles/releases) ships static
binaries for Linux, macOS and Windows, with a `checksums.txt`.

```
herakles run      -config FILE [-once [-report FILE]] [-target NAME] [-log-level LEVEL]
herakles validate -config FILE
herakles version
```

| Flag | |
|---|---|
| `-once` | Run one cycle of every target and exit: 0 if every required step passed, 1 otherwise. Does not start the metrics server. |
| `-report FILE` | With `-once`: write a JUnit XML report, one test case per step. See [In CI pipelines](#in-ci-pipelines). |
| `-target NAME` | Probe only this target. Handy for testing locally without touching production. |
| `-log-level` | `debug`, `info` (default), `warn` (only failures) or `error`. |

### In CI pipelines

The same journeys that run in the cluster make a post-deploy smoke test. Run
one cycle against the environment you just deployed and publish the report:

```groovy
// Jenkinsfile
stage('Smoke test') {
  steps {
    sh 'herakles run -once -target staging -config herakles.yaml -report herakles.xml'
  }
  post { always { junit 'herakles.xml' } }
}
```

Each target and labor becomes a test suite, and each step becomes a test case:

| Step result | In the report | In Jenkins |
|---|---|---|
| ok | passed | green |
| required step failed | `<failure type="required">` | red, and `-once` exits 1 |
| optional step failed | `<failure type="optional">` | unstable, a warning |
| skipped, or its labor was skipped | `<skipped>` with the reason | grey |

Failure messages are the redacted reasons from the log, plus the trace id.

On SIGTERM, herakles cancels the running step. Then it runs the pending
`finally` blocks and finalizers, so that nothing is left behind and the session
is closed, and exits.

## Metrics

All metrics are prefixed `herakles_` and labeled `target`, plus `labor`,
`step`, `phase` or `direction` where they apply.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `step_duration_seconds` | histogram | target, labor, step | Duration of every step attempt. |
| `step_last_duration_seconds` | gauge | target, labor, step | Exact duration of the last attempt. |
| `step_success` | gauge | target, labor, step | 1 if the last attempt passed, 0 if it failed. Includes optional steps. |
| `step_skipped` | gauge | target, labor, step | 1 if the step was skipped in its last cycle. |
| `labor_success` | gauge | target, labor | 1 if the last run of the labor passed. |
| `labor_last_success_timestamp_seconds` | gauge | target, labor | When the labor last passed. Use it to detect a stalled probe. |
| `labor_duration_seconds` | histogram | target, labor | Duration of every labor run, finally included. |
| `labor_last_duration_seconds` | gauge | target, labor | Exact duration of the last run. |
| `labor_failed_step` | gauge | target, labor, step | 1 on the required step that failed the last run, 0 on the others. |
| `http_phase_seconds` | gauge | target, labor, step, phase | Phases of the last HTTP request: `dns`, `connect`, `tls`, `wait` (request sent to first byte), `transfer`, `total`. On a reused connection `dns`, `connect` and `tls` are 0. |
| `transfer_bytes_per_second` | gauge | target, labor, step, direction | Rate of the last `upload` or `download`. |
| `build_info` | gauge | version, goversion | Always 1. Shows which herakles version runs where: `herakles_build_info{version!="0.2.0"}`. |
| `cycle_success` | gauge | target | 1 if every required step of the last cycle passed. It is 0 when variables cannot be resolved, for example an unreadable secret file. |

A skipped labor does not update its series. Its last success timestamp ages,
which is exactly what a staleness alert should catch.

### Why exact gauges next to histograms

Probe requests often take a few milliseconds. With histogram buckets, every
quantile inside the same bucket is interpolated, so steps of 3 ms and 9 ms can
both show a p95 of 10 ms. Use histograms for rate-style alerts. For real
latency, use the exact last-value gauges, and for percentiles over time:

```promql
quantile_over_time(0.95, herakles_step_last_duration_seconds{target="prod"}[1h])
quantile_over_time(0.95, herakles_http_phase_seconds{phase="wait"}[1h])
```

Example alerts (failing labor, stale labor, failing cycle, slow step, slow
transfer) are in
[`examples/kubernetes/monitoring/prometheusrule.yaml`](examples/kubernetes/monitoring/prometheusrule.yaml).

### Health

`/health` answers 200 while the process is alive. It does not depend on probe
results on purpose: an unhealthy target must raise an alert, not make
Kubernetes restart the probe.

## Logs

One line per event. The fixed-width left zone (time, level, target, labor,
position, step) stays aligned in `kubectl logs`. The message comes after a
`|`, and each event kind starts with a greppable marker:

| Marker | Event |
|---|---|
| `>>` | Step starts, with its description and the variables it needs. |
| `ok` | Step passed, with what it set and its transfer rate. |
| `~~` | An optional step failed; the labor goes on. |
| `!!` | A required step failed; the labor stops. |
| `--` | Step skipped, with the reason. |
| `==` | Labor or cycle summary. |

Tokens, secret variables and request bodies are never logged. Variable values
never reach the logger, only their names. Error messages pass through a
redactor that masks every secret, also in its URL-encoded and JSON-escaped
forms.

## Security

herakles treats its config as trusted: the operator's own input. It treats
everything that comes back from the probed services as untrusted.

- **Secrets stay out of the config and out of the output.** They are read from files or the environment. Variable values never reach the logger. Every error is masked for each secret, raw, URL-encoded and JSON-escaped.
- **No template injection.** Templates come only from the config and run once per step. A captured value that contains `{{ ... }}` stays literal text.
- **No header injection.** Go rejects header values with line breaks, and herakles rejects them in multipart part headers.
- **No log injection.** Every event is one line: line breaks in messages and attributes are replaced.
- **No credential leaks on redirects.** Headers carrying secrets are dropped when a redirect leaves the host.
- **Bounded work.** Every step has a timeout. Response bodies are streamed, and only 1 MiB (for JSONPath) or 200 bytes (for error messages) is kept. A panicking module fails its step, not the process.
- **The container**, as shipped in the example manifests: static binary on distroless, non-root, read-only root filesystem, no capabilities, no service account token.

## Tracing

Every request carries a [W3C `traceparent`](https://www.w3.org/TR/trace-context/)
header, so services instrumented with OpenTelemetry put herakles traffic in
their traces:

- **One trace per labor run.** Every step of the journey shares the trace id, so Tempo or Jaeger shows the whole journey together.
- **One span id per step.** The backend's spans hang from the step that called them.
- **Sampled** (`-01`). Probe requests are few and worth keeping, even where the backend samples its own traffic.

Failure lines in the log carry the trace id. Copy it into your tracing UI to
see which service failed and why:

```
!! FAILED after 1.02s: status 502, want 2xx; body: upstream timeout  trace=0af7651916cd43dd8448eb211c80319c
```

herakles does not export spans of its own. The backend shows the step's
requests with a parent span it never received, which Tempo and Jaeger display
normally. To send your own header instead, set `traceparent` in `headers`.

## Running in Kubernetes

[`examples/kubernetes`](examples/kubernetes) has a kustomization with a
Deployment, a generated ConfigMap, an example Secret, a Service, a
ServiceMonitor and alerts, plus a demo in kind or k3d. Points worth keeping:

- **One replica, by design.** Two instances would run the same mutating steps at once and step on each other. The Deployment uses `replicas: 1` and the `Recreate` strategy, so rollouts never overlap either.
- **Secrets as files.** Mount the Secret and reference it with `{file: ...}`. herakles needs no Kubernetes API access, so the pod gets no service account token.
- **Locked down.** The pod runs as non-root, on a read-only root filesystem, without capabilities.
- **Small.** RSS stays around 20 MB even while moving 5 MiB payloads. `GOMEMLIMIT` is derived from the container limit. The image is a static binary on distroless: 18 MB on disk, under 6 MB to pull.

The image is published to GHCR for `linux/amd64` and `linux/arm64` as
`ghcr.io/raulnid/herakles`. The eurystheus demo API is not published: it is
only for demos and tests, and the Kubernetes demo builds it locally.

## Writing a module

A module is Go code compiled into the binary. It needs no change to the core:
implement the interface, register it, import the package in `cmd/herakles`.

```go
package tcpcheck

import (
	"context"
	"net"

	"github.com/raulnid/herakles/internal/module"
)

type Config struct {
	Addr string `yaml:"addr"`
}

type Module struct{}

func init() { module.Register("tcp", Module{}) }

func (Module) NewConfig() any { return &Config{} }

func (Module) Run(ctx context.Context, cfg any, env *module.Env) module.Result {
	c := cfg.(*Config)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return module.Result{Err: err}
	}
	conn.Close()
	return module.Result{}
}
```

The core renders templates and decodes the step config into `NewConfig()`.
It applies the timeout through `ctx`, measures the duration, and turns the
`Result` into logs and metrics. A module never sees templates, logs or metrics.

| `Result` field | Becomes |
|---|---|
| `Err` | Step failure. Secrets are masked before logging. |
| `Captured` | Variables for later steps (`vars.Value{Raw, Secret}`). |
| `Phases` | `herakles_http_phase_seconds{phase=...}`. |
| `Transfers` | `herakles_transfer_bytes_per_second{direction=...}`. |

Optional interfaces:

- `Validator` adds static checks to `herakles validate`. Templated string fields hold their template text at that point, and templated typed fields (ints, durations) are zero. Skip format checks on them with `module.Templated(s)`. Validation also runs before `run`. It is not called again per request, so keep runtime checks in `Run` to what a template can break.
- `Outputs` names the variables a step sets. Validation uses it to check references, and the runner uses it to skip the steps that need the outputs of a skipped step.
- `BulkTransfer` puts a step under `transfer_timeout`.

Add the module's keys to the JSON Schema under `$defs` and `step`.

This contract covers the modules planned next: **grpc** (phases, captures from
response fields), **s3** (transfers, with payloads as the object body),
**websocket** (connect, first message, transfer), **tcp** (connect and TLS
phases) and **exec** (captures from stdout).

## Design

```
cmd/herakles          CLI, wiring, /metrics and /health
internal/config       YAML types and structural checks
internal/vars         per-cycle variables, file/env sources, secret redaction
internal/tmpl         template rendering on the YAML tree, variable references
internal/engine       per-target loops, cycles, labors, finally, finalizers, validate
internal/module       the module contract and registry
internal/modules/     http (httpstep) and payload
internal/metrics      Prometheus observer
internal/logfmt       log observer and fixed-width handler
internal/junit        JUnit report observer for -once
examples/             journey, eurystheus mock API, Kubernetes manifests
schema/               JSON Schema of the config
```

- **The engine emits events, observers report them.** Metrics and logs are two implementations of `engine.Observer`, so neither knows about the other, and tests record events directly.
- **The config tree is rendered, then decoded.** Templates are expanded on the YAML nodes and the result is decoded into the module's own struct. Modules get native Go types and stay template-free.
- **Built for a long-running process.** Templates and JSONPath expressions are parsed once and cached, and per-step metadata is computed when the engine starts. Everything else that could carry state, such as variables, the HTTP client, connections and cookies, is rebuilt every cycle, so no cycle can depend on the previous one.
- **Failures stay contained.** A panicking module fails its step, not the process. Every step is bounded by a timeout. Cleanup and finalizers survive shutdown.

## Known limits

- **No load testing.** Each labor runs once per cycle. Concurrency per labor is planned, with an isolated variable map for each copy.
- **No leftover sweep.** If the process is killed hard (SIGKILL, node loss) between creating and deleting a resource, the resource stays. There are no loops or filters in the YAML yet to sweep `synthetic-*` leftovers.
- **Payload uploads use chunked encoding.** Servers that require a `Content-Length` on uploads are not supported yet.
- **Probes from inside the cluster.** herakles probes your services from inside the cluster. It does not cover the client's network path: their DNS, proxies or certificates.
- **Redaction works on text.** A secret shorter than 4 bytes is not masked (a warning is logged). A secret transformed by the server in other ways, such as base64 or partially, cannot be recognized.

## Development

```sh
go vet ./... && go test -race ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run
```

The tests run every module against fake servers (`httptest`) and include
failure cases:

- a checksum mismatch;
- an endpoint that is down;
- a timeout;
- an optional step that fails;
- cleanup after a failure;
- a finalizer that runs after another labor failed;
- a mutating step skipped on a safe target;
- secrets echoed back by the server.

The example journey also runs end to end against the mock, both as a Go test
and in CI between two containers.

## License

[Apache-2.0](LICENSE)
