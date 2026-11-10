// Package forwardedhost provides a Caddy request matcher that routes by the
// effective host of a request: the rightmost value of the last X-Forwarded-Host
// field when the request comes from a trusted proxy, or from any peer when
// TrustAnyPeer is set, otherwise the inbound Host.
package forwardedhost

import (
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func init() {
	caddy.RegisterModule(MatchForwardedHost{})
}

// VarKey is the request variable holding the authority used for matching,
// readable as {http.vars.forwarded_host}. It is set only on a successful match
// and may be empty or stale elsewhere.
const VarKey = "forwarded_host"

// MatchForwardedHost matches requests by their effective host: the
// X-Forwarded-Host header when the peer is a trusted proxy, else the inbound Host.
// TrustAnyPeer replaces the trusted-proxy condition with "any peer".
//
// Trust is whatever the server's trusted_proxies option already decided, except
// when TrustAnyPeer bypasses that verdict for this matcher. The module never
// reads or re-parses proxy configuration, and never writes the verdict back.
//
// On a successful match it publishes the authority it used as VarKey.
type MatchForwardedHost struct {
	// Hosts follows the same rules as the built-in host matcher: wildcards
	// occupying a whole label, placeholders, case-insensitive, request ports ignored.
	Hosts []string `json:"hosts,omitempty"`

	// RequireHeader rejects requests without a valid X-Forwarded-Host instead of
	// falling back to the inbound Host. By default it also requires the server to
	// have judged the peer trusted; with TrustAnyPeer set, that half drops and only
	// the header condition remains.
	RequireHeader bool `json:"require_header,omitempty"`

	// TrustAnyPeer uses X-Forwarded-Host from any peer, bypassing the server's
	// trusted_proxies verdict for this matcher only. The trusted_proxy request
	// variable, client_ip and reverse_proxy keep the verdict the server computed.
	//
	// Any client that can connect can then choose its route by writing the header.
	// Only enable it when ingress is restricted to the proxy by something a client
	// cannot forge (firewall rules, socket permissions, an authenticating sidecar)
	// and the proxy overwrites client-supplied X-Forwarded-Host. Configure real
	// proxy ranges instead whenever that is possible.
	TrustAnyPeer bool `json:"trust_any_peer,omitempty"`

	hosts caddyhttp.MatchHost
}

// CaddyModule returns the Caddy module information.
func (MatchForwardedHost) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.matchers.forwarded_host",
		New: func() caddy.Module { return new(MatchForwardedHost) },
	}
}

// Provision validates the configuration and prepares the inner host matcher.
func (m *MatchForwardedHost) Provision(ctx caddy.Context) error {
	if len(m.Hosts) == 0 {
		return fmt.Errorf("forwarded_host matcher requires at least one host")
	}
	for _, host := range m.Hosts {
		if host == "" {
			return fmt.Errorf("forwarded_host matcher: empty host is not allowed")
		}
		if err := validateWildcardPlacement(host); err != nil {
			return err
		}
	}
	m.hosts = caddyhttp.MatchHost(m.Hosts)
	return m.hosts.Provision(ctx)
}

// MatchWithError implements caddyhttp.RequestMatcherWithError.
func (m MatchForwardedHost) MatchWithError(r *http.Request) (bool, error) {
	trusted, _ := caddyhttp.GetVar(r.Context(), caddyhttp.TrustedProxyVarKey).(bool)
	selected, state := "", forwardedAbsent
	if trusted || m.TrustAnyPeer {
		selected, state = selectForwardedHost(r.Header.Values("X-Forwarded-Host"))
	}

	switch state {
	case forwardedInvalid:
		// Present but unusable: never fall back, or a bad proxy value would
		// silently route elsewhere.
		return false, nil
	case forwardedAbsent:
		if m.RequireHeader {
			return false, nil
		}
	}

	target, authority := r, r.Host
	if state == forwardedPicked {
		authority = selected
	}

	// Caddy v2.10.2 matches a list over 100 entries with a byte-wise exact lookup and
	// stops the fallback loop at the first non-fuzzy entry, so exact entries bypass
	// the case-insensitive comparison. Lowercasing here keeps config entries written
	// in lowercase matchable in any request case; the trade-off is that on v2.10.2 an
	// entry containing uppercase cannot match in such a list at all. v2.11.9 lowercases
	// the request host and normalizes exact entries in Provision, so both work there.
	// The published authority keeps its original case.
	if matchHost := strings.ToLower(authority); matchHost != r.Host {
		rr := *r
		rr.Host = matchHost
		target = &rr
	}

	matched, err := m.hosts.MatchWithError(target)
	if matched && err == nil && authority != "" {
		caddyhttp.SetVar(r.Context(), VarKey, authority)
	}
	return matched, err
}

type forwardedState int

const (
	// forwardedAbsent: no usable header, so the inbound Host applies.
	forwardedAbsent forwardedState = iota
	// forwardedPicked: a valid authority was selected.
	forwardedPicked
	// forwardedInvalid: the header has content but no usable value.
	forwardedInvalid
)

// selectForwardedHost picks the rightmost comma-separated item of the last
// X-Forwarded-Host field, i.e. the value recorded by the nearest trusted hop.
func selectForwardedHost(values []string) (string, forwardedState) {
	// Every field blank is the same as the header being absent (ADR §4). Fields are
	// checked individually: joining them first would turn a multi-byte character
	// split across two fields into something TrimSpace could treat as whitespace.
	if !slices.ContainsFunc(values, func(v string) bool { return strings.TrimSpace(v) != "" }) {
		return "", forwardedAbsent
	}

	item := values[len(values)-1]
	if i := strings.LastIndexByte(item, ','); i >= 0 {
		item = item[i+1:]
	}
	item = strings.TrimSpace(item)
	if !validAuthority(item) {
		return "", forwardedInvalid
	}
	return item, forwardedPicked
}

// validAuthority reports whether s is plausible as the host[:port] of a
// request target. It is an input boundary, not a DNS validator: name matching
// is left to caddyhttp.MatchHost.
func validAuthority(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
		switch r {
		case '\\', '*', '@', '/', '?', '#':
			return false
		}
	}

	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return false
		}
		addr, err := netip.ParseAddr(s[1:end])
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return false
		}
		rest := s[end+1:]
		if rest == "" {
			return true
		}
		if !strings.HasPrefix(rest, ":") {
			return false
		}
		return validPort(rest[1:])
	}

	if strings.ContainsAny(s, "[]") {
		return false
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		if i == 0 || strings.Count(s, ":") > 1 {
			return false
		}
		return validPort(s[i+1:])
	}
	return true
}

func validPort(port string) bool {
	// Five digits also rejects zero-padded values such as "000080", which
	// ParseUint would otherwise read as 80.
	if len(port) == 0 || len(port) > 5 {
		return false
	}
	n, err := strconv.ParseUint(port, 10, 16)
	return err == nil && n >= 1
}

// validateWildcardPlacement rejects wildcards that do not occupy a whole label
// (e.g. "*-app.example.com"), which MatchHost could never match.
func validateWildcardPlacement(host string) error {
	if !strings.Contains(host, "*") {
		return nil
	}
	for _, label := range strings.Split(host, ".") {
		if strings.Contains(label, "*") && label != "*" {
			return fmt.Errorf("forwarded_host matcher: host %q has a wildcard that does not occupy a whole label; use *.example.com", host)
		}
	}
	return nil
}

var (
	_ caddy.Provisioner                 = (*MatchForwardedHost)(nil)
	_ caddyfile.Unmarshaler             = (*MatchForwardedHost)(nil)
	_ caddyhttp.RequestMatcherWithError = (*MatchForwardedHost)(nil)
)
