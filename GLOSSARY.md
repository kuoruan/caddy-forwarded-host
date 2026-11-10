# Glossary

**Effective host**: The single host identity a request is routed by. It comes from one source only, never from a combination of sources. _Avoid_: original domain, proxied host

**Inbound host**: The `Host` header as received, before any forwarding consideration. This is what site-block selection and certificates use. _Avoid_: real host (it is not more "real" than the forwarded value; name the intended one)

**X-Forwarded-Host (XFH)**: The hostname carried by that forwarding header, recorded by the nearest proxy hop. This project always uses the header name or the abbreviation, never a paraphrase. _Avoid_: forwarded host (the module is called `forwarded_host`; the header value is XFH), original domain (on a multi-hop chain it need not be the original public hostname)

**Trusted proxy**: A directly connected peer declared by the server's `trusted_proxies` option, whose forwarding headers may be believed. _Avoid_: allowlist, permitted range

**Host split**: Dispatching requests to different backends by effective host, as opposed to routing by origin hostname or path. _Avoid_: domain routing, vhost routing

**Matched-host variable**: The request variable `forwarded_host`, read as `{http.vars.forwarded_host}`. It holds the authority the matcher used, published only on a successful match. _Avoid_: effective host placeholder (it is request state, not a global placeholder)
