# Glossary

**Effective host**: The single host identity a request is routed by. It comes from one source only, never from a combination of sources. _Avoid_: original domain, proxied host

**Inbound host**: The `Host` header as received, before any forwarding consideration. This is what site-block selection and certificates use. _Avoid_: real host (it is not more "real" than the forwarded value; name the intended one)

**X-Forwarded-Host (XFH)**: The hostname the header carries, as recorded by the nearest proxy hop. This project always uses the header name or the abbreviation, never a paraphrase. _Avoid_: forwarded host (the module is called `forwarded_host`; the header value is XFH), original domain (on a multi-hop chain it need not be the original public hostname)

**Trusted proxy**: A directly connected peer declared by the server's `trusted_proxies` option, whose forwarding headers may be believed. _Avoid_: allowlist, permitted range
