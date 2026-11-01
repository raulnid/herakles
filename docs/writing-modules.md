# Writing a module

A module is Go code compiled into the binary. It needs no change to the core:
implement the interface, register it from `init`, import the package in
`cmd/herakles`. The smallest real module,
[`internal/modules/tcpstep`](../internal/modules/tcpstep/tcp.go), is the
reference: about 100 lines with validation, phases and TLS.

```go
type Module interface {
	NewConfig() any                                   // pointer to a config struct with yaml tags
	Run(ctx context.Context, cfg any, env *Env) Result
}

func init() { module.Register("tcp", Module{}) }
```

The core renders templates and decodes the step config into `NewConfig()`.
It applies the timeout through `ctx`, measures the duration, and turns the
`Result` into logs and metrics. A module never sees templates, logs or metrics.

| `Result` field | Becomes |
|---|---|
| `Err` | Step failure. Secrets are masked before logging. |
| `Captured` | Variables for later steps (`vars.Value{Raw, Secret}`). |
| `Phases` | `herakles_step_phase_seconds{phase=...}`. |
| `Transfers` | `herakles_transfer_bytes_per_second{direction=...}`. |
| `Cert` | `herakles_tls_cert_expiry_timestamp_seconds` and `..._not_before_...`. Fill it with `module.FirstToExpire(&connState)`. |

`Env` gives a module the target name, the variables (read-only), the cycle's
HTTP client with keep-alive and cookies, its `TLSConfig` (the target's CA,
client cert and `insecure_skip_verify`; clone it before changing it), the user
agent and a W3C `traceparent`.

Optional interfaces:

- `Validator` adds static checks to `herakles validate`. Templated string fields hold their template text at that point, and templated typed fields (ints, durations) are zero. Skip format checks on them with `module.Templated(s)`. Validation also runs before `run`. It is not called again per request, so keep runtime checks in `Run` to what a template can break.
- `Outputs` names the variables a step sets. Validation uses it to check references, and the runner uses it to skip the steps that need the outputs of a skipped step.
- `BulkTransfer` puts a step under `transfer_timeout`.

Add the module's keys to the JSON Schema under `$defs` and `step`.

This contract covers the modules planned next: **grpc** (phases, captures from
response fields), **s3** (transfers, with payloads as the object body),
**websocket** (connect, first message, transfer) and **exec** (captures from
stdout).
