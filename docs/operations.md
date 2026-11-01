# Operations

## Command line

Every [release](https://github.com/raulnid/herakles/releases) ships static
binaries for Linux, macOS and Windows, with a signed `checksums.txt`. See
[Verifying releases](security.md#verifying-releases).

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

[`examples/kubernetes`](../examples/kubernetes) has a kustomization with a
Deployment, a generated ConfigMap, an example Secret, a Service, a
ServiceMonitor and alerts, plus a demo in kind or k3d. Points worth keeping:

- **One replica, by design.** Two instances would run the same mutating steps at once and step on each other. The Deployment uses `replicas: 1` and the `Recreate` strategy, so rollouts never overlap either.
- **Secrets as files.** Mount the Secret and reference it with `{file: ...}`. herakles needs no Kubernetes API access, so the pod gets no service account token.
- **Locked down.** The pod runs as non-root, on a read-only root filesystem, without capabilities.
- **Small.** RSS stays around 20 MB even while moving 5 MiB payloads. `GOMEMLIMIT` is derived from the container limit. The image is a static binary on distroless: 18 MB on disk, under 6 MB to pull.

The image is published to GHCR for `linux/amd64` and `linux/arm64` as
`ghcr.io/raulnid/herakles`. The eurystheus demo API is not published: it is
only for demos and tests, and the Kubernetes demo builds it locally.
