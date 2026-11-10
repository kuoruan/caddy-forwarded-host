# Project context

Maintainer notes. Usage is documented in [`README.md`](README.md) and [`README.zh-CN.md`](README.zh-CN.md), design rationale in [`docs/adr/0001-forwarded-host-matcher.md`](docs/adr/0001-forwarded-host-matcher.md), and terminology in [`GLOSSARY.md`](GLOSSARY.md).

A Caddy request matcher, `http.matchers.forwarded_host` (Caddyfile name `forwarded_host`), that routes by effective host: the nearest-hop `X-Forwarded-Host` when the peer is a trusted proxy, otherwise `Host`.

## Layout

```
forwardedhost.go       module, effective-host selection, authority validation
caddyfile.go           UnmarshalCaddyfile
*_test.go              table tests and benchmarks; integration_test.go runs a real in-process Caddy
docs/adr/              decision records (the only place design is argued)
```

One direct dependency: `github.com/caddyserver/caddy/v2 v2.10.2`. No handler, no registration beyond `init()`. The receiver holds configuration plus the provisioned inner `hosts caddyhttp.MatchHost`; no per-request state may be stored there.

## How it works

1. `prepareRequest` runs before routing and writes the server's `trusted_proxies` verdict into `trusted_proxy`; the matcher reads it with `caddyhttp.GetVar(...).(bool)`. Trust is never configured or re-parsed here (ADR §2). The only override is `trust_any_peer`, which ORs into that read without writing the variable back (ADR §10).
2. The matcher collapses the request to exactly one authority, then delegates hostname comparison to `caddyhttp.MatchHost` on a shallow copy with only `Host` replaced. Wildcards, placeholders, IDNA, case folding and port stripping are upstream behavior rather than this module's (ADR §5).
3. On a successful match it publishes that authority as the request variable `forwarded_host` (`{http.vars.forwarded_host}`), which is how `header_up Host` can follow a wildcard or list config (ADR §9).

## Invariants

Changing any of these is a design change, so update the ADR first.

- One source per request: no OR of `Host` and XFH, and a trusted-but-invalid header never falls back to `Host` (ADR §3, §4).
- Value selection reads the last header field's rightmost comma item only; no leftward search.
- The output variable is written only when `matched && err == nil`; a failed match neither writes nor clears it.
- `Provision` rejects an empty host list, empty entries, and any `*` that does not occupy a whole label; all remaining validation is delegated to `MatchHost`.
- No matcher-level trust configuration, no handler, no request mutation. `trust_any_peer` holds no ranges and writes nothing back; it declines to consult the shared verdict for this matcher (ADR §10), so XFH can be trusted here while `reverse_proxy` still treats the same peer as untrusted.

## Verification

```bash
GOTOOLCHAIN=go1.25.0 go vet ./... && GOTOOLCHAIN=go1.25.0 go test ./...
```

The toolchain pin is local-only: Caddy 2.10.x panics on config load when built with Go 1.27+ (`encoding/json.RawMessage` became an alias of `jsontext.Value`, breaking Caddy's reflect-based module-map check). 2.11.x is unaffected, so no pin is needed there.

Forward compatibility was verified against v2.11.9; CI pulls the newest Caddy v2 release on every run. Run the upgrade in a scratch copy, because it mutates `go.mod`:

```bash
cp -a . /tmp/fhcompat && cd /tmp/fhcompat
GOTOOLCHAIN=auto go get github.com/caddyserver/caddy/v2@latest
GOTOOLCHAIN=auto go mod tidy
go list -m -f '{{.Version}}' github.com/caddyserver/caddy/v2   # v2.11.9 when verified
GOTOOLCHAIN=auto go test ./...
```

The integration test starts a real Caddy through `caddytest`, so `prepareRequest` runs before matching: `:9081` carries `trusted_proxies static 127.0.0.1/32`, `:9082` carries none, and `:9083` sets `trust_any_peer` with no `trusted_proxies`. All three are exercised in one run. The `-short` skip comes from `caddytest` itself, not from this repository.

## Traps

- Hosts must be on one line. Repeated matcher lines merge only inside one named-matcher block (Caddy concatenates same-name matchers into a single token stream and `RemainingArgs` stops at end-of-line); on one line the module name parses as a host, and defining the same `@name` twice is a Caddyfile error. Pinned by adapter-level tests; the user-facing rule is documented in the README.
- The host handed to `MatchHost` is lowercased first. In Caddy v2.10.2 a list over 100 entries is matched by a byte-wise exact lookup and the fallback loop stops at the first non-fuzzy entry, so exact entries bypass the case-insensitive comparison. Lowercasing buys case-insensitive matching for lowercase config entries; the trade-off is that on v2.10.2 an entry containing uppercase cannot match in such a list, even in identical case. v2.11.9 lowercases the request host and normalizes exact entries in `Provision`, so both forms match there. Pinned by `TestLargeListMatchesHostDelegation`. Only the matching value is lowercased; the published authority keeps its case.
- The shallow request copy does not escape, so it costs no allocation, only the struct copy. Do not replace it with a temporary mutation of `r.Host`; not modifying the request is part of the contract (ADR §5). `benchmark_test.go` measures the hot path, including the upstream `MatchHost` baseline this module delegates to.
- Config entries must not carry a port: `MatchHost` strips ports on the request side only, so such an entry parses and never matches (pinned by a test).
- Request-derived placeholders in the config are unsupported and unhardened: the replacer runs before the `*` check, so a request can inject a wildcard.
- User-facing behavior belongs in the README, internals and traps here, and argument in the ADR.
