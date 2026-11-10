# caddy-forwarded-host

English | [简体中文](README.zh-CN.md)

A Caddy request matcher that routes by the effective host of a request: by default the `X-Forwarded-Host` value when the request comes from a trusted proxy, otherwise the inbound `Host`. The opt-in `trust_any_peer` accepts the header from any peer.

Useful when a CDN or edge proxy (Cloudflare Workers, a load balancer, another Caddy) forwards requests for many public hostnames to a single origin hostname, and routing has to follow the public hostname without trusting values a client can set.

## Relation to the built-in `header` matcher

`@app header X-Forwarded-Host app.example.com` trusts the header unconditionally. `X-Forwarded-Host` is a plain client-supplied header: any client that can reach the origin directly can set it. By default this module only uses it when Caddy has already decided the peer is a trusted proxy, using the server's existing [`trusted_proxies`](https://caddyserver.com/docs/caddyfile/options#trusted-proxies) option. This adds no extra trust configuration and no second source of truth; the `trust_any_peer` exemption described below must be enabled explicitly.

## Install

```bash
xcaddy build --with github.com/kuoruan/caddy-forwarded-host
```

## Prerequisites

- The peer must be listed in the server's `trusted_proxies`; that option is the default trust model. Without it the matcher behaves like the built-in `host` matcher, which is a valid configuration. The only exception is `trust_any_peer`, described in [Bypassing the peer check](#bypassing-the-peer-check).
- The request still has to enter a site block, and site blocks are selected by the inbound `Host`, not by `X-Forwarded-Host`. The site address must therefore cover the host sent by clients (see [Boundaries](#boundaries)).

## Usage

Snippets without a site address are fragments to be placed inside a site block.

```caddyfile
{
	servers {
		trusted_proxies static 173.245.48.0/20 103.21.244.0/22
	}
}

origin.example.com {
	@app forwarded_host app.example.com
	handle @app {
		reverse_proxy 127.0.0.1:8081
	}

	handle {
		respond "Not Found" 404
	}
}
```

Host lists and wildcards follow the same rules as the built-in `host` matcher: wildcards must occupy a whole label, placeholders are supported, matching is case-insensitive, and a port carried by the *request* is ignored. Configured hosts should omit ports. Entries containing ports are accepted but never match, as with the built-in matcher.

```caddyfile
@app forwarded_host app.example.com app2.example.com
```

Value selection from the header: the rightmost comma-separated item of the last `X-Forwarded-Host` field, that is, the value written by the hop closest to this server. If that item is empty or is not a valid `host[:port]`, the request does not match. There is no leftward search and no fallback to the inbound `Host`.

### Writing the matched host to the backend

The backend receives the origin `Host` unless the configuration changes it. When the matched host is known only at runtime (`*.example.com` or a list), reference the variable this matcher publishes on a successful match:

```caddyfile
@apps forwarded_host *.example.com

handle @apps {
	reverse_proxy 127.0.0.1:8081 {
		header_up Host {http.vars.forwarded_host}
	}
}
```

`{http.vars.forwarded_host}` is the authority that was actually used for matching: the `X-Forwarded-Host` value when the peer is trusted or `trust_any_peer` is on, otherwise the inbound `Host`. It keeps the port.

- It is only set when this matcher matched: use it inside that route. Referenced from a catch-all route it is empty, or still holds the value from an earlier successful match in the same request. `header_up Host` with an empty value makes Go fall back to the upstream address, so the backend receives `127.0.0.1:8081`, not the public host.
- It can be overwritten by a `vars` directive later in the same route, and a `vars` matcher must not read it from the same matcher set (matcher order inside a set is not deterministic).
- Wrapping the matcher in `not` still publishes it when the inner match succeeds: the negation happens after this module ran.
- It is a reflection of the request, not an authorization decision.

### Strict mode

By default a request without a usable `X-Forwarded-Host` falls back to the inbound `Host`, which supports deployments where one site block serves both direct and proxied traffic. Add `require_header` to accept only requests that came through a trusted proxy *and* carried a valid `X-Forwarded-Host` (with `trust_any_peer` on, only the header condition applies):

```caddyfile
@app forwarded_host app.example.com {
	require_header
}
```

Configured hosts belong on the matcher's own line; the block accepts `require_header` and `trust_any_peer`.

| Request | default | `require_header` |
| --- | --- | --- |
| peer untrusted, any `X-Forwarded-Host` | match on `Host` | no match |
| peer trusted, header missing or blank | match on `Host` | no match |
| peer trusted, valid header | match on it | match on it |
| peer trusted, valid header that matches nothing | no match (never falls back to `Host`) | no match |
| peer trusted, header present but invalid | no match | no match |

This table assumes `trust_any_peer` is off; when it is on, the rows for an untrusted peer are handled as if the peer were trusted (see [Bypassing the peer check](#bypassing-the-peer-check)).

### Bypassing the peer check

If the origin is reachable only through the proxy, the proxy address may not be expressible as a range, and its egress range may change. `trust_any_peer` makes this matcher use `X-Forwarded-Host` from any peer:

```caddyfile
@app forwarded_host *.example.com {
	trust_any_peer
}
```

It affects this matcher only. `client_ip`, the access log and `reverse_proxy` keep the verdict the server computed from `trusted_proxies`, and that verdict is never written back.

The flag delegates the routing decision to any client that can connect: any client can send `X-Forwarded-Host` and select a route this matcher allows; the authority checks only reject malformed values. Enable it only when ingress is restricted to the proxy by something a client cannot forge, such as firewall rules, socket file permissions or an authenticating sidecar, and when the proxy overwrites any client-supplied `X-Forwarded-Host` instead of passing it through. A listener on a private network is not sufficient, because every other host on that network can forge the header. Listing the proxy ranges remains the recommended configuration.

With the flag on the peer no longer matters, so `require_header` reduces to "a valid `X-Forwarded-Host` must be present":

| Request, `trust_any_peer` on | default | `require_header` |
| --- | --- | --- |
| header missing or blank | match on `Host` | no match |
| valid header | match on it | match on it |
| valid header that matches nothing | no match (never falls back to `Host`) | no match |
| header present but invalid | no match | no match |

### JSON

```json
{
  "forwarded_host": {
    "hosts": ["app.example.com", "app2.example.com"]
  }
}
```

Add `"require_header": true` to enable strict mode, or `"trust_any_peer": true` to accept the header from any peer.

## Scenarios

### Many public hostnames, one origin

A proxy forwards many tenant domains to one origin hostname; the tenant list is known at runtime, and issuing a certificate per tenant is not an option.

```caddyfile
origin.example.com {
	@tenants forwarded_host *.example.com

	handle @tenants {
		reverse_proxy 127.0.0.1:8081 {
			header_up Host {http.vars.forwarded_host}
		}
	}

	handle {
		respond "unknown tenant" 404
	}
}
```

This uses one site block and one certificate for `origin.example.com`; the matcher derives the tenant hostname from the `X-Forwarded-Host` written by the trusted proxy and passes it to the backend as `Host`.

### Cloudflare Worker that rewrites `Host`

The Worker overwrites `Host` with the origin hostname and keeps the original in a single `X-Forwarded-Host` value. List Cloudflare's ranges in `trusted_proxies` (the `http.ip_sources.cloudflare` module can supply them, at the cost of an extra build dependency, from [`github.com/WeidiDeng/caddy-cloudflare-ip`](https://github.com/WeidiDeng/caddy-cloudflare-ip)), and match on the public name as in the first example.

### Migration: old and new hostname side by side

```caddyfile
@both forwarded_host app.example.com app.example.net
handle @both {
	reverse_proxy 127.0.0.1:8081 {
		header_up Host {http.vars.forwarded_host}
	}
}
```

Both names route to the same backend and each keeps its own `Host`; retiring the old name requires changing one token rather than duplicating a route.

### Split by public hostname to different backends

```caddyfile
origin.example.com {
	@blog forwarded_host blog.example.com
	handle @blog {
		reverse_proxy 127.0.0.1:8081
	}

	@shop forwarded_host shop.example.com
	handle @shop {
		reverse_proxy 127.0.0.1:8082
	}

	handle {
		respond "unknown host" 404
	}
}
```

The split follows the value that passed the trust check, so blue/green or per-application routing requires swapping a `handle` block rather than changing certificates and site addresses.

### Direct and proxied traffic in one site block

```caddyfile
# Direct visitors keep working: the inbound Host is the public name.
@direct forwarded_host app.example.com
handle @direct {
	reverse_proxy 127.0.0.1:8081
}

# Proxied traffic only: a trusted peer and a valid X-Forwarded-Host are required.
@proxied forwarded_host app.example.com {
	require_header
}
handle @proxied {
	reverse_proxy 127.0.0.1:8081
}
```

Use `require_header` only for hostnames that must never be reached directly.

### Non-applicable scenario: several hostnames, one backend, no split

```caddyfile
a.example.com, b.example.com {
	reverse_proxy 127.0.0.1:8081
}
```

If no logic depends on which hostname was used, a site-address list is sufficient. This matcher adds a dependency on `trusted_proxies` and does not enable direct access to a third hostname, because it cannot change site selection.

## Boundaries

- Site-block selection and TLS certificates still use the inbound `Host` and SNI. The matcher decides routing only inside an already selected site block. Serving `app.example.com` directly requires the site address to cover it (`*.example.com` or a list), which usually means a wildcard certificate obtained with the DNS-01 challenge.
- It does not rewrite `r.Host`; use `header_up Host {http.vars.forwarded_host}` (or a literal) to change what the backend sees.
- `trust_any_peer` takes the trust decision away from `trusted_proxies` for this matcher: a client that can reach the origin chooses the route, and the authority checks only reject malformed values.
- It is not an authorization mechanism. Trusting Cloudflare's ranges does not imply trusting the value a Worker writes into the header; `require_header` does not protect against a proxy that passes a client-supplied `X-Forwarded-Host` through.
- Only `X-Forwarded-Host` is handled. `X-Forwarded-For` belongs to `trusted_proxies` / `client_ip`; `X-Forwarded-Proto` does not affect host matching and does not restore TLS/SNI.
- Placeholders in the host list are intended for static sources (environment or configuration). A request-derived placeholder such as `forwarded_host {http.request.header.X-Tenant-Host}` lets the request inject a wildcard into the matcher; that usage is unsupported and unhardened.
- A trailing-dot FQDN (`app.example.com.`) passes validation but does not match `app.example.com`, and does not fall back to `Host`.
- Static file servers and shared caches key on `Host` + URL, not on `X-Forwarded-Host`: if responses are shared between hosts, send `Vary: X-Forwarded-Host` or disable caching.

## Troubleshooting

A trusted peer whose `X-Forwarded-Host` is present but invalid is hard to diagnose: the matcher does not fall back, so the request appears as an ordinary 404 and the module logs nothing. To narrow it down:

1. Send the request with an explicit header from an address inside `trusted_proxies` (for local testing, temporarily add the address used by the test client): `curl -H 'Host: origin.example.com' -H 'X-Forwarded-Host: app.example.com' https://origin.example.com/`
2. Check Caddy's access log. Request headers are logged there, and each entry records the site block that handled the request.
3. Compare with the same request without the header: if that request matches, the header value is the cause (empty rightmost item, trailing dot, or a non-authority value such as a full URL).

## Compatibility

Built against Caddy v2.10.2 (Go 1.25) and tested against v2.11.9.

Caddy 2.10.x cannot load any configuration when built with Go 1.27+, because of an upstream `encoding/json` change (`json.RawMessage` becoming an alias). Use Caddy 2.11.x on Go 1.27+, or build 2.10.x with Go 1.25/1.26. Details are in [CONTEXT.md](CONTEXT.md).
