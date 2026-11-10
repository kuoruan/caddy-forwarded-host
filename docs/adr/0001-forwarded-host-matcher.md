# Route by effective host with a matcher, reusing the server's `trusted_proxies` as the trust boundary

Status: accepted (2026-11-10)

## Background

The origin sits behind a trusted proxy (a Cloudflare Worker) that forcibly rewrites `Host`, so the original hostname can only be recovered from `X-Forwarded-Host` (XFH). The existing workaround is a CEL expression, `host('app.example.com') || header({'X-Forwarded-Host': 'app.example.com'})`: the hostname is written twice, and the client-controlled header is believed unconditionally.

This repository provides a Caddy request matcher that routes by effective host. An earlier internal proposal for this module was superseded by this record in several places, listed under "Differences from the earlier proposal" below.

## Decision

### 1. A matcher only, no handler

Module ID `http.matchers.forwarded_host`, Caddyfile name `forwarded_host`.

The goal is to route by Host/XFH, not to rewrite the request. A matcher has no directive-ordering burden (a third-party handler needs the global `order` option or a `route` wrapper to be sequenced) and it cannot disturb other handlers. Neither form can change the selection of an outer site block, so "rewrite `r.Host` to influence site-block selection" is not available to either of them and is not an argument for a handler.

### 2. The trust boundary is the server's `trusted_proxies`, with one documented exemption

```go
trusted, _ := caddyhttp.GetVar(r.Context(), caddyhttp.TrustedProxyVarKey).(bool)
```

Only `true` makes the header count; a missing variable, a wrong type, or `false` all mean "do not trust it". The one exception is the opt-in `trust_any_peer` flag, which bypasses this input entirely without writing it back (§10).

Caddy computes the verdict in `prepareRequest` (in `modules/caddyhttp/server.go`, before routing) and stores it in the request context variable `trusted_proxy` (key defined in the same file), readable through `caddyhttp.GetVar` (`modules/caddyhttp/vars.go`). The matcher therefore never touches server configuration at Provision time and never loads IP range sources itself.

By default users declare the boundary once, in the global options, and `reverse_proxy`, the `client_ip` matcher and this matcher all share it:

```caddyfile
{
	servers {
		trusted_proxies static 173.245.48.0/20 103.21.244.0/22
	}
}
```

Provider ranges can come from `http.ip_sources.*` modules (for example `http.ip_sources.cloudflare`), at the cost of an extra build dependency.

### 3. One effective host, not an OR of two sources

| Peer | XFH | Matching input |
| --- | --- | --- |
| untrusted | anything | `r.Host` |
| trusted | missing/blank | `r.Host` |
| trusted | valid | that value (no fallback to `r.Host`) |
| trusted | present but invalid | no match |

An OR behind a trust gate is equally safe, so the objection is not forgeability. The problem is that OR can match both the origin hostname and the X-Forwarded-Host name at once, which leaves route priority indescribable and the configuration unpredictable.

`require_header` turns the first two fallback rows into "no match" and leaves the other two unchanged (§8). `trust_any_peer` removes the peer column altogether: every request is treated as trusted, so the three XFH rows decide alone (§10).

### 4. XFH value: the rightmost comma item of the last header field

Selection is a fixed sequence; the first step that applies wins:

1. Read `Header.Values("X-Forwarded-Host")`. Header missing, or every field blank after trimming → treat as absent, use `r.Host`.
2. Take the last field, split on `,`, take the rightmost item and trim it.
3. Empty item (a trailing comma, for example) → no match, no leftward search, no fallback to `r.Host`.
4. Not a valid authority → no match, same as above.
5. Valid → match on it alone, even if `r.Host` would also match.

| Situation | Result |
| --- | --- |
| header missing; or `""`, `["", ""]` | absent, use `r.Host` |
| `evil.example, app.example.com` | selects `app.example.com` |
| `", "` (trims to `","`, not blank); `app.example.com,`; or fields `["app.example.com", ""]` | rightmost item empty → no match |
| literal `*.example.com` | `*` is a pattern, not a host value → no match |
| trusted valid XFH that does not match while `r.Host` would | still no match |

A "valid authority" is an input boundary only, decidable item by item:

- Non-empty, with no `unicode.IsSpace` or `unicode.IsControl` characters, and no backslash, `*`, `@`, `/`, `?`, `#`.
- Starting with `[`: a matching `]` must follow, its content must be non-empty and parse via `netip.ParseAddr` as `Is6()`. Zones are rejected (`[fe80::1%eth0]` is invalid). Only `:port` may follow the `]`.
- Not starting with `[`: no `[` or `]`, at most one `:`, and a non-empty host part when a `:` is present.
- With a port: one to five ASCII decimal digits, value 1 to 65535; an empty port after `:` is invalid.
- No trailing-dot normalization: `app.example.com.` passes validation, but `MatchHost` does not handle trailing dots, so it does not match the `app.example.com` entry and does not fall back to `r.Host`.

No DNS character-set validation, no request-side IDNA, no trailing-dot normalization. Hostname matching follows `MatchHost` semantics: it normalizes IDNA on the configuration side only (`idna.ToASCII` uses `idna.Punycode`, which rejects little beyond malformed `xn--` payloads), which is not the same as validating DNS names, and this module promises nothing beyond it.

Placeholders: `MatchHost` calls `repl.ReplaceAll(host, "")` before testing for `*` (`modules/caddyhttp/matchers.go`), and the replacer is bound to the original request. A config entry built from a request-derived placeholder (such as `forwarded_host {http.request.header.X-Tenant-Host}`) therefore lets a `*` from the request reach the wildcard branch, bypassing both the request-side `*` rejection and the Provision check; `{http.request.host}` yields the inbound `Host`, not the effective host. Such usage is unsupported and unhardened; no interception is added.

Configuration-side wildcards and placeholders come straight from `MatchHost`: `forwarded_host *.example.com` matches `app.example.com` (`*` covers exactly one label), and `{...}` placeholders work. The request side rejects wildcards, which is policy rather than syntax (RFC 3986 `reg-name` allows `*`): a forwarded header carries a concrete host for this request, not a pattern. Without the rejection, `MatchHost` skipping `*` labels would let the literal `*.example.com` match the `*.example.com` entry.

Intra-label globs are unsupported: `MatchHost` only skips comparison when an entire label equals `*` (`modules/caddyhttp/matchers.go`), so `*-app.example.com` and `app*.example.com` can never match. Such entries fail at Provision instead of being accepted and silently never matching. Any entry containing `*` must have every `*`-containing label exactly `*`; `{...}` placeholders are unaffected. When "unbounded, grouped by suffix" is needed, list the known hostnames (`a-app.example.com b-app.example.com`); open-ended suffix matching is a different capability and would deserve its own `forwarded_host_regexp` rather than turning `*` into another glob dialect.

Both the common proxy behavior of appending (each hop appends its own value) and the Worker's overwrite-with-a-single-value work. "Rightmost" means the authority recorded by the nearest hop; restoring the leftmost original public hostname of a multi-hop chain is not promised. A proxy must either overwrite XFH or append a non-empty value of its own; a trusted peer does not guarantee that the header is truthful.

### 5. Hostname matching is fully delegated to `caddyhttp.MatchHost`

`MatchWithError` shallow-copies the request, replaces only the copy's `Host`, and calls `m.hosts.MatchWithError(&copy)`, returning its error unchanged. The original request, headers, URL and context are untouched.

Before delegating, the host we hand over is lowercased. In Caddy v2.10.2 a list longer than 100 entries is matched by a byte-wise binary search for exact entries, and the fallback loop stops at the first non-fuzzy entry, so the case-insensitive comparison never runs for them (`modules/caddyhttp/matchers.go`). Lowercasing buys case-insensitive matching for config entries written in lowercase, which native 2.10.2 matches only when the request case is identical. The trade-off is that a config entry containing uppercase cannot match in such a list on v2.10.2, even when the request uses the identical case, because `MatchHost.Provision` there only applies IDNA conversion and uses lowercasing for duplicate detection (`modules/caddyhttp/matchers.go`). Caddy v2.11.9 changed this: it lowercases the request host and normalizes exact config entries in `Provision`, so both forms match and the lowercasing here is redundant but harmless. The difference is upstream behavior and is documented rather than corrected, since rewriting config entries here would change matching semantics. The published authority keeps its original case.

Case folding, IDNA, wildcards, placeholders, deduplication, port stripping and large-list sorting all otherwise come from upstream `MatchHost` (`modules/caddyhttp/matchers.go`). This module adds no request-side IDNA or trailing-dot normalization. `MatchHost` normalizes IDNA on the configuration side only, and on the request side it strips the port and IPv6 brackets and compares case-insensitively.

### 6. Configuration surface: a flat host list, booleans as block options

```go
type MatchForwardedHost struct {
	Hosts         []string `json:"hosts,omitempty"`
	RequireHeader bool     `json:"require_header,omitempty"`
	TrustAnyPeer  bool     `json:"trust_any_peer,omitempty"`

	hosts caddyhttp.MatchHost
}
```

```caddyfile
@app forwarded_host app.example.com app2.example.com

@app2 forwarded_host app.example.com {
	require_header
	trust_any_peer
}
```

```json
{
  "forwarded_host": {
    "hosts": ["app.example.com", "app2.example.com"],
    "require_header": true,
    "trust_any_peer": true
  }
}
```

The first use of `forwarded_host` is still a space-separated host list; the block exists only for the boolean options, which are `require_header` (§8) and `trust_any_peer` (§10). Reading a block is a small switch on the subdirective name, and each option refuses a second occurrence. `UnmarshalCaddyfile` is written here: upstream concatenates same-name instances into one token stream (`caddyconfig/httpcaddyfile/httptype.go`) and `RemainingArgs` stops at end-of-line (`caddyconfig/caddyfile/dispenser.go`), so only one instance per line merges correctly; repeating on one line (`forwarded_host a.com forwarded_host b.com`) does not error but parses the literal `forwarded_host` as a host. `Provision` does three things: error on empty `Hosts` or an empty entry; reject intra-label `*` (a wildcard must occupy a whole label); hand the rest to `MatchHost.Provision`.

The cost is giving up "delegate to `MatchHost.UnmarshalCaddyfile` in one line" and "JSON identical to the built-in `host` matcher". The switch-free flat-slice alternative is recorded as rejected in Considered Options.

### 7. No custom logging, rate limiting, or startup warnings

Without trusted proxies configured, the matcher degrades to the built-in `host` behavior; that is a legitimate use, not a configuration error, and Provision cannot reliably learn the actual server's trust configuration, so any warning would be a false positive. Observability belongs to Caddy's access logs.

A trusted peer with an invalid XFH is hard to diagnose: the matcher does not fall back, so the request simply does not match and looks like a plain 404. Debugging relies on access logs (the request headers are still logged), not on module logging.

Rewriting the backend `Host` is not automatic, but on a successful match the matcher stores the authority it used in the request variable `forwarded_host`, and the config writes it explicitly with `header_up Host {http.vars.forwarded_host}` (see §9). Using `header_up Host {http.request.header.X-Forwarded-Host}` instead is wrong: empty on direct requests, garbage when multi-valued, and it bypasses the trust decision.

### 8. `require_header` (default `false`)

The meaning is fixed: the peer must be trusted and a valid `X-Forwarded-Host` must exist. It is not "any forwarding header exists", and it is not a relaxation of trust. The peer-trust requirement is the server's verdict unless `trust_any_peer` is set (§10), in which case only the header-presence requirement remains.

| Request | default `false` | `true` |
| --- | --- | --- |
| untrusted peer, any XFH content | match on `r.Host` | no match |
| trusted peer, XFH missing or all blank | match on `r.Host` | no match |
| trusted peer, selected valid XFH matches | match | match |
| trusted peer, valid XFH does not match (even if `r.Host` would) | no match | no match |
| trusted peer, XFH has content but the selected item is invalid | no match | no match |

Why default `false`: it is exactly the goal of "one routing rule for both direct and proxied entry". With the flag off by default, the matcher cannot serve any plain direct request, and users would have to add a `host` route again, which brings the duplicated configuration straight back. The fallback does not weaken security either: `Host` is a client-chosen field, so a direct client writing `Host: app.example.com` achieves the same as writing a forged XFH. The flag controls "does direct traffic get in", which is a deployment policy, not a trust boundary.

The name is `require_header` rather than `require_forwarded`: the latter reads as "all forwarding headers (XFH/XFF/XFP) must be present". `require_header` alone is not specific enough, so the full semantics must be documented in prose and godoc.

The field is `RequireHeader bool`; the boolean zero value is exactly the default, so no pointer or defaulting constructor is needed.

### 9. Match output: the request variable `forwarded_host`

A static `header_up Host <literal>` only works for a single configured hostname; with `*.example.com` or a host list the target is known only at runtime, and echoing the raw XFH back is not acceptable. The matcher therefore publishes the authority it actually used, on a successful match:

```caddyfile
@apps forwarded_host *.example.com

handle @apps {
	reverse_proxy 127.0.0.1:8081 {
		header_up Host {http.vars.forwarded_host}
	}
}
```

The write is `caddyhttp.SetVar(r.Context(), "forwarded_host", host)`; the placeholder is `{http.vars.forwarded_host}` (`modules/caddyhttp/vars.go`, `replacer.go`). This follows the convention of Caddy's regexp matcher publishing capture groups (`modules/caddyhttp/matchers.go`, implemented with `repl.Set`), except it goes through vars and does not occupy the global placeholder namespace.

Output contract:

- Trust and the three-state decision, the `require_header` gate and `MatchHost.MatchWithError` all run first; the write happens only when the match succeeded and there was no error.
- Trusted XFH: the trimmed, authority-validated value, with its port kept (e.g. `a.example.com:8443`).
- Default-mode direct request / no XFH: the `r.Host` that actually took part in matching, never an empty string.
- A concrete authority, never the config pattern `*.example.com` and never the first element of `Hosts`.
- On failure, no invalid value is written and a value from an earlier successful match is not cleared.
- `r.Host`, headers, URL and global state are untouched; only request-scoped variables are written.

Scope (request state, not a global placeholder):

- It is meaningful only inside a handle that this matcher matched. In a catch-all route, in a purely `not` route, or where the matcher never ran, it is empty or still holds the result of an earlier match. Passing an empty value to `header_up Host` leaves `r.Host` empty, after which Go falls back to `r.URL.Host` (the upstream address), so the backend receives something like `127.0.0.1:8081`, not an empty string and not the public hostname.
- It is an echo of the routing input, not an authorization credential.
- Wrapping the matcher in `not` does not prevent the publish: negation happens after this module ran, so an inner success still leaves the value behind.
- Nothing is published when the authority used for matching is empty (a placeholder resolving to nothing against a request with no `Host`).
- A `vars forwarded_host ...` directive can overwrite it (handlers run after matching, so the user's value wins).
- Matcher order inside one matcher set is not deterministic (`MatcherSet` is built from a JSON map), so a `vars` matcher must not read this output in the same set.

The side effect is contained: the published value is a function of the request and of this instance's configuration. Two instances with different `trust_any_peer` settings can publish different values for the same request, so nothing may assume the variable reflects the server's verdict. Repeating the evaluation on one instance is idempotent, and nothing per-request is stored on the receiver.

### 10. `trust_any_peer`: an explicit exemption from the trust verdict

Some deployments cannot state their ingress as an IP range. The origin may be reachable only through the proxy (private network, firewall, a sidecar that terminates mTLS), the proxy's egress range may move, or an IP range source module may not be worth an extra build dependency. For those, `trust_any_peer` makes this matcher use `X-Forwarded-Host` from any peer.

The header-selection gate is the whole implementation:

```go
trusted, _ := caddyhttp.GetVar(r.Context(), caddyhttp.TrustedProxyVarKey).(bool)
if trusted || m.TrustAnyPeer {
	selected, state = selectForwardedHost(r.Header.Values("X-Forwarded-Host"))
}
```

It does not write the `trusted_proxy` variable back, does not change `client_ip`, and does not change what `reverse_proxy` does with `X-Forwarded-*`. The shared variables stay exactly as `determineTrustedProxy` computed them (in `modules/caddyhttp/server.go`, called from `prepareRequest` there); only this matcher's reading of them differs.

It is an exemption, not a second trust configuration: no ranges, no range sources, no per-site lists to keep in sync with the server. This separates it from the matcher-level `trusted_proxies` field rejected in Considered Options. It does accept a deliberate local divergence: with the flag on, a request the server judged untrusted still routes on XFH here, while `reverse_proxy` and `client_ip` keep treating that same request as untrusted. That divergence is a matcher-local policy change, it is confined to this matcher instance, and it is deliberate.

Nothing else about matching changes. Authority validation (space, control characters, `*`, `@`, `/`, `?`, `#`), rightmost-item selection, no fallback when the selected value is invalid, wildcard rules and port retention all behave as described in §4 and §5. The flag only decides which requests reach that code.

Composition with `require_header` (§8): with the flag off, an untrusted peer never enters the XFH path; with the flag on, the peer-trust requirement is satisfied by every peer, so `require_header` reduces to "X-Forwarded-Host must be present and valid". A missing header still falls back to `Host` in default mode and is still rejected under `require_header`.

With the flag on, any client that can open a connection and send `X-Forwarded-Host` selects among the routes this matcher allows; the authority checks only reject malformed values. Reachability alone is not a sufficient condition: a listener on a private interface remains reachable by every other host on that network. The flag is safe only when ingress is restricted to the proxy by something a client cannot forge (firewall rules, socket file permissions, an authenticating sidecar) and the proxy overwrites any client-supplied XFH instead of passing it through. A proxy range that changes frequently is an argument for convenience, not for security. The option remains a deployment workaround, off by default, with configuring the real ranges as the recommendation.

The name follows Caddy's own `trusted_proxies_unix`, which also names the additional source that gets trusted rather than calling itself `insecure`. `insecure` was rejected here because inside this matcher it would not say what is insecure, and it could be read as disabling authority validation, which always stays on. `skip_trust_check` was rejected because it describes the mechanism rather than the resulting policy, and because the check itself lives in Caddy core; this module only ignores its result.

## Outer routing boundary

This matcher only works inside a route that was already entered:

- Site addresses (a site block's host matching) are still selected by the inbound `Host`; `X-Forwarded-Host` plays no part, and this module cannot change that.
- TLS certificates are still selected by SNI; `strict_sni_host` behavior is unaffected.
- The backend's `Host` does not change by itself: it still needs `header_up Host {http.vars.forwarded_host}` (or a literal for a single host).

That is its role as a matcher: fine-grained routing inside a site block, not a site selector, and not a "choose the certificate by XFH" scheme.

## Considered Options

- A handler that rewrites `r.Host`: makes the built-in `host`, `{host}` and `reverse_proxy`'s default Host all correct, but the site block has already been chosen, so it only works in the subset where the site was already correct, and it invites the false impression that it can select a site. Users would also have to deal with directive ordering. Rejected.
- A matcher-level `trusted_proxies` field: self-contained and allows per-site trust ranges, but it forks from the server-level verdict: the same request is trusted to `reverse_proxy` and untrusted to the matcher, which is hard to debug. Rejected. The `trust_any_peer` flag in §10 is not this design: it holds no ranges and no second policy list, and it declines to consult the shared verdict for one matcher instance, accepting the divergence §10 describes.
- Dual-source OR semantics (the CEL-equivalent status quo): no defensible priority. Rejected.
- Rejecting the whole header when XFH is multi-valued: more conservative, but it breaks the standard append chain, and the Worker's overwrite form only ever produces a single value. Rejected.
- Naming it `effective_host`: more accurate, but people searching for `X-Forwarded-Host` would not find it, and a registered module ID is irreversible; discoverability wins. Rejected.
- A switch-free flat slice: JSON identical to the built-in `host` matcher and a one-line `UnmarshalCaddyfile` delegate, at the cost of losing the "only accept trusted forwards" mode. The struct plus block option was chosen because that mode is a separate and reasonable routing policy, and is less error-prone than asking users to hand-build the trust variable, header presence and emptiness checks in CEL.
- Naming the flag `require_forwarded`: reads as requiring all forwarding headers. Rejected in favor of `require_header`.
- Intra-label globs (`*-app.example.com`): would require our own pattern parsing, Provision validation, case/IDNA interactions and an extra matching path, turning the module from "delegates to `MatchHost`" into a matching engine of its own, while upstream only treats a whole label `*` as a wildcard. Rejected; such configs now error at Provision.
- A matcher-level `trusted_proxies` that overrides or extends the server's: not in v1. `reverse_proxy`'s local `TrustedProxies` extends rather than overrides (it reads the server's `trusted_proxy` first and then sets `trusted = true` if a local CIDR matches, `modules/caddyhttp/reverseproxy/reverseproxy.go`), so it is not a precedent for "local overrides top-level". A local list could only influence whether this matcher uses XFH; it could not change the `client_ip` the server computed, nor `reverse_proxy`'s trust in forwarding headers, so two policies would coexist and users would have to understand both. If "different proxy sets for different routes on one listener" ever becomes real, it should be explicit replacement: unset → inherit the server's; non-empty → use only that list; explicitly empty → trust no proxy at all; check only the direct peer's `RemoteAddr`, never infer from XFF/`client_ip`, and never mutate shared variables.
- Intercepting request-derived placeholders in the config: `MatchHost` replaces placeholders before testing for `*` (`modules/caddyhttp/matchers.go`), so a request-derived placeholder brings `*` into the wildcard branch. Rejecting any entry containing `{` would close it, but only by giving up all placeholders; v1 documents "unsupported, unhardened" instead.
- Also supporting `X-Forwarded-For` / `X-Forwarded-Proto`: different jobs. The client IP is already handled by `trusted_proxies` + the `client_ip` matcher, and the protocol is irrelevant to host matching; cramming all three into one module would only muddle the trust semantics. Rejected.

## Consequences

- Baseline dependency is Caddy v2.10.2, verified up to v2.11.9 (the newest v2 at the time); the implementation uses `RequestMatcherWithError`. The APIs required are identical on both versions, so compatibility is not bought by lowering the implementation.
- `trusted_proxies` is a server-level setting that applies to the whole server and is no longer a private matcher setting.
- Trusting Cloudflare's ranges only identifies "from Cloudflare's network", not "from the operator's own Worker"; other tenants on those ranges are trusted too, and `Host` is still chosen by the client. This module is therefore not an authorization mechanism: origin firewalls and authentication remain necessary.
- The definition of effective host (port stripping, case insensitivity, wildcard rules) always follows upstream `MatchHost`; upstream changes will show up here directly, which is intentional.
- Only `X-Forwarded-Host` is handled: `X-Forwarded-For` belongs to `trusted_proxies` / `client_ip`, and `X-Forwarded-Proto` is irrelevant to host matching and does not change the actual TLS/SNI/certificate selection or restore the original protocol.
- Routing by XFH while the response cache key stays `Host` + URL means the same URL can hit different backends depending on XFH; shared caches (Cloudflare cache, a future cache handler) need `Vary: X-Forwarded-Host` or no caching.
- `require_header` does not protect against "a trusted proxy passes the client's own XFH through": it only requires a trusted peer and a valid header. The proxy must overwrite XFH or append correctly.
- `trust_any_peer` is the one setting that lets this module route on a header written by any client. It is off by default and must carry its two conditions wherever it appears: ingress restricted to the proxy by something a client cannot forge, and a proxy that overwrites client-supplied `X-Forwarded-Host`. Users who only need "trust the proxy" should configure its ranges, not this flag.
- The match output (the `forwarded_host` variable) comes from the request: it is an echo of the routing input, it can be overwritten by a same-request `vars` directive, and it can be empty in a route that did not match. Do not use it for authorization.

## Sources and verification boundary

The following facts were checked read-only against a local module cache, at Caddy v2.11.9; paths are relative to that module root:

| Fact | Source |
| --- | --- |
| `prepareRequest` computes the trusted-proxy verdict into a context variable before routing | `modules/caddyhttp/server.go` |
| `TrustedProxyVarKey = "trusted_proxy"`, `ClientIPVarKey = "client_ip"` | `modules/caddyhttp/server.go` |
| `caddyhttp.GetVar(ctx, key)` reads context variables | `modules/caddyhttp/vars.go` |
| `MatchHost` type, `Provision` (config-side IDNA, dedup, sort), `Match`/`MatchWithError` (request-side port/bracket stripping, case-insensitive compare, wildcards, placeholders) | `modules/caddyhttp/matchers.go` |
| Named matcher dynamic lookup for `http.matchers.<name>`, asserting `caddyfile.Unmarshaler` and `RequestMatcher`/`RequestMatcherWithError` | `caddyconfig/httpcaddyfile/httptype.go` |
| Core `http.ip_sources` only has `static`; `trusted_proxies` is server-level and loaded through IP range source modules | `modules/caddyhttp/ip_range.go`, `modules/caddyhttp/server.go` |
| `remote_ip` reads only `r.RemoteAddr`; `client_ip` reads the trusted-proxy-derived `client_ip` variable | `modules/caddyhttp/ip_matchers.go` |

External references:

- [Extending Caddy](https://caddyserver.com/docs/extending-caddy): module IDs must be snake_case; once registered, a module appears on the official download page.
- [caddyserver/caddy#3599](https://github.com/caddyserver/caddy/issues/3599): closed 2020-08-21; mholt explicitly rejected building XFH support in to save a line of config.
- `https://caddyserver.com/api/packages` (fetched 2026-11-10, 335 packages): no registered XFH-related module; `http.ip_sources.*` has 11 entries, all IP range sources.

Verification boundary: everything above is read-only source and documentation review. The implementation has since been tested (see `CONTEXT.md` for the commands); no end-to-end test against a real external proxy was performed.

### Version policy: baseline dependency v2.10.2, verified through v2.11.9

`go.mod` requires `github.com/caddyserver/caddy/v2 v2.10.2` and the implementation uses `RequestMatcherWithError`. The baseline is 2.10.2 rather than the latest v2.11.9 because of Go module resolution: requiring v2.11.9 would make MVS upgrade a user's explicit Caddy 2.10 to 2.11.9, at which point claiming 2.10 support would be false. The code is identical on both versions and gains no version branches.

Per-tag source comparison (`https://raw.githubusercontent.com/caddyserver/caddy/<tag>/...`):

| Capability | v2.6.2 | v2.6.3 | v2.9.0 | v2.10.2 | v2.11.9 |
| --- | --- | --- | --- | --- | --- |
| Server-level `trusted_proxies` (`TrustedProxiesRaw`) + Caddyfile option | no | yes | yes | yes | yes |
| `http.ip_sources.static` | no | yes | yes | yes | yes |
| `trusted_proxy` variable written to the request context before routing | no | yes | yes | yes | yes |
| `caddyhttp.GetVar` | yes | yes | yes | yes | yes |
| `MatchHost.MatchWithError` / `RequestMatcherWithError` | no | no | yes | yes | yes |

The `go` directives of both versions (read from each tag's `go.mod`): v2.10.2 is `go 1.25`, v2.11.9 is `go 1.26.0`. This module therefore declares `go 1.25.0`; building it against 2.11.9 is handled by the consumer's toolchain and needs no change here. Upgrading to a newer release raises the `go` directive to whatever that release requires and needs `GOTOOLCHAIN=auto`; such a change is temporary and must not be committed.

Conclusion: the `trusted_proxy` variable is available from v2.6.3 and `MatchWithError` from v2.9.0; 2.10.2 and 2.11.9 differ in nothing this module needs. Reaching back to 2.6 through 2.8 would require switching to `Match` and giving up error reporting, so it is not supported.

`determineTrustedProxy` returns a single `bool` in v2.6.3 (v2.10+ returns `(bool, string)`), but the variable name and type are the same, so `GetVar(...).(bool)` works on both.

## Differences from the earlier proposal

The earlier internal proposal was superseded in these places: a module-level `trusted_proxies` field; picking the rightmost non-empty value with fallback to `r.Host`; debug logging and a startup warning; no Caddyfile subdirective; and undecided empty-`hosts` semantics. Its object-shaped JSON is also superseded: the config is still an object, but with `hosts` + `require_header`, and trust stays at the server level.
