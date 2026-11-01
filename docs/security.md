# Security

herakles treats its config as trusted: the operator's own input. It treats
everything that comes back from the probed services as untrusted.

- **Secrets stay out of the config and out of the output.** They are read from files or the environment. Variable values never reach the logger. Every error is masked for each secret, raw, URL-encoded and JSON-escaped.
- **Client keys are secrets too.** `key_file` is read from disk every cycle and never logged: errors name the file, never its content.
- **No template injection.** Templates come only from the config and run once per step. A captured value that contains `{{ ... }}` stays literal text.
- **No header injection.** Go rejects header values with line breaks, and herakles rejects them in multipart part headers.
- **No log injection.** Every event is one line: line breaks in messages and attributes are replaced.
- **No credential leaks on redirects.** Headers carrying secrets are dropped when a redirect leaves the host.
- **Bounded work.** Every step has a timeout. Response bodies are streamed, and only 1 MiB (for JSONPath) or 200 bytes (for error messages) is kept. A panicking module fails its step, not the process.
- **The container**, as shipped in the example manifests: static binary on distroless, non-root, read-only root filesystem, no capabilities, no service account token.

## Verifying releases

Release images and checksums are signed in CI with
[cosign](https://github.com/sigstore/cosign) keyless signing: the certificate
says which workflow, on which tag, of this repository signed them, and the
signature is logged in the public Rekor transparency log. There is no key to
leak. Pre-releases (`v0.3.0-rc.1`) are signed the same way, so these commands
work for them too: change the version.

The image is signed by digest. Verifying a tag resolves it to its digest:

```sh
cosign verify ghcr.io/raulnid/herakles:0.3.0 \
  --certificate-identity-regexp '^https://github\.com/raulnid/herakles/\.github/workflows/ci\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The signed index also covers the SBOM and the SLSA provenance that buildx
attaches to it:

```sh
docker buildx imagetools inspect ghcr.io/raulnid/herakles:0.3.0 --format '{{ json .SBOM }}'
docker buildx imagetools inspect ghcr.io/raulnid/herakles:0.3.0 --format '{{ json .Provenance }}'
```

For the binaries, download `checksums.txt`, `checksums.txt.sigstore.json` and
your archive from the release, verify the checksums file, then the archive
against it:

```sh
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/raulnid/herakles/\.github/workflows/ci\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing checksums.txt
```

Releases up to v0.2.0 predate signing.
