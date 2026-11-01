# Configuration

One YAML file, which can be mounted from a ConfigMap. A JSON Schema at
[`schema/herakles.schema.json`](../schema/herakles.schema.json) gives your editor
completion and validation. With the YAML extension in VS Code, add this first
line:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/raulnid/herakles/main/schema/herakles.schema.json
```

`herakles validate -config FILE` checks a config without running anything. It
checks the structure, the module configs and template syntax, and that every
variable a step reads is declared or set by an earlier step, for every target.
`run` performs the same checks before it starts.

## Global settings

| Key | Default | |
|---|---|---|
| `interval` | `60s` | Time between the start of two cycles of a target. A cycle that overruns starts the next one right away, never two at once. |
| `step_timeout` | `10s` | Time limit for each step. |
| `transfer_timeout` | `120s` | Time limit for steps that upload a payload or verify a download (`assert.sha256` or `assert.size`). |
| `listen` | `:9090` | Address of `/metrics` and `/health`. |
| `user_agent` | `herakles/<version> (synthetic probe)` | Sent on every request, so the traffic can be filtered out of your stats. |

## Variables

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
| `{{ .item }}` | The current item, in a step with [`foreach`](#sweeping-leftovers-with-foreach). |
| `{{ .vars.id \| pathescape }}` | The value escaped for a URL path segment. |
| `{{ .vars.q \| urlquery }}` | The value escaped for a query string. |
| `{{ .vars.id \| sha256 }}`, `sha512` | The hex digest of the value, for APIs keyed by a hash. |

Values captured from responses are inserted as they are. Escape them when they
go into a URL, so an id like `a/b?x` cannot change the path or the query:
`/projects/{{ .vars.project_id | pathescape }}`.

How templates render:

- A field that is exactly one variable keeps that variable's type. `size: "{{ .vars.upload.size }}"` stays a number, and `{"api_key": "{{ .vars.key }}"}` stays a string even when the key looks like `0123`.
- A field that mixes text and templates always renders to a string.
- Reading an undefined variable fails the step. A finally or finalizer step is skipped instead.

## Targets

```yaml
targets:
  - name: prod
    safe: true                          # skip mutating steps
    vars: {base_url: {value: "https://api.example.com"}}
    tls:
      ca_file: /etc/ssl/internal-ca.pem # added to the system roots, read every cycle
      insecure_skip_verify: false
      cert_file: /etc/herakles/tls/tls.crt # client cert for mTLS, with key_file
      key_file: /etc/herakles/tls/tls.key
```

All TLS files are read again at the start of every cycle. A short-lived client
certificate (cert-manager, step-ca) rotated on disk is used from the next cycle
on, without a restart.

In Kubernetes, mount the Secret as a volume, as the example deployment does.
The kubelet updates the files of a mounted Secret in the running pod, usually
within a minute or two, so no restart and no reloader are needed. That does not
hold for a Secret in an environment variable or mounted with `subPath`: those
never change until the pod restarts. The kubelet swaps the files atomically,
but cert and key are two reads: a cycle that lands exactly on the swap can fail
once with a key mismatch, and the next one loads the new pair. cert-manager
renews at two thirds of the lifetime, so the old pair is still valid meanwhile. `cert_file` and `key_file` go together; `validate`
checks that, but not that the files exist. If a file is missing or does not
load, the cycle of that target fails (`herakles_cycle_success` 0, the error
with the file path in the log) and every other target keeps running, so a
Secret that does not exist yet cannot stop the other probes.

## Labors and steps

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

### Sweeping leftovers with `foreach`

A process killed hard (SIGKILL, node loss) between creating and deleting a
resource leaves it behind, and `finally` never runs. Sweep at the start of the
next cycle instead: capture every match of a JSONPath as a list with
`all: true`, then run a step once per item with `foreach`. The item is
`{{ .item }}` (or `{{ .item.id }}` when items are objects).

```yaml
      - name: list
        http:
          url: "{{ .vars.base_url }}/projects"
          capture:
            leftovers: {jsonpath: "$[?(@.name =~ /^synthetic-/)].id", all: true}
      - name: sweep
        mutating: true
        optional: true                  # a failed sweep is a warning, not an outage
        foreach: leftovers
        http:
          method: DELETE
          url: "{{ .vars.base_url }}/projects/{{ .item | pathescape }}"
          assert: {status: [204, 404]}  # 404: already gone
```

- The step reports one result: ok when every item passes, including when the list is empty. The first failing item stops it, and the error names it (`item 3 of 7: ...`).
- Each item gets its own `step_timeout`. Phases and transfer rates are those of the last item.
- `validate` checks that the list variable exists and that `.item` is only read inside `foreach`.
- Pick a name pattern that only the probe uses. herakles runs one replica, so nothing it did not create matches `synthetic-*` unless you share the prefix.

## The `http` module

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
    ids:     {jsonpath: "$[*].id", all: true}  # every match, as a list for foreach
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
- **Checks for one status** go in `assert.on_status`. Some APIs answer an empty search with a 404 and a message: accept both codes, and require the message only on the 404, so a 404 from a missing route still fails.
  ```yaml
  assert:
    status: [200, 404]
    on_status:
      404: {jsonpath: {$.message: "Patient not found"}}
  ```
  Each code must be one the step accepts. `on_status` only holds `jsonpath` for now.
- **JSONPath** uses the full [JSONPath](https://goessner.net/articles/JsonPath/) syntax via [ojg](https://github.com/ohler55/ojg). A capture takes the first match, or every match as a list with `all: true`. Responses used by `jsonpath` are parsed up to 1 MiB.
- **Downloads** are streamed: the body is hashed and counted as it arrives and never kept whole in memory. The hash is computed only when `sha256` or `sha256_header` is set. See [Verifying downloads](#verifying-downloads).
- **Error messages** include at most the first 200 bytes of the response body, with every secret masked.
- **Redirects** are followed, up to 10. On a redirect to another host, every header that carries a secret is dropped, not just `Authorization` and `Cookie` as Go does by default. A presigned storage URL gets the request; your API key does not.
- **Sessions**: a cycle uses one HTTP client per target, with keep-alive and a cookie jar, like a real client. The connection is reused within the cycle and dropped at its end.

## Payloads

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

## Verifying downloads

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

## The `tcp` module

Checks that a port accepts connections, and with `tls: true` that the TLS
handshake succeeds. Use it for services that do not speak HTTP: databases,
brokers, SMTP, LDAPS.

```yaml
- name: postgres-tls
  tcp:
    addr: "db.internal:5432"   # host:port, templated like any field
    tls: true                  # CA, client cert and insecure_skip_verify come from the target's tls
    server_name: db.internal   # SNI and the name verified; default: the host of addr
```

It reports the `dns`, `connect`, `tls` and `total` phases in
`herakles_step_phase_seconds`, and with `tls: true` the certificate expiry in
`herakles_tls_cert_expiry_timestamp_seconds`, so expiry alerts also cover
non-HTTP services. It sends no data: a TLS service that needs STARTTLS (SMTP
on 587, PostgreSQL) is checked at the TCP level only.
