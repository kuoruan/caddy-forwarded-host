package forwardedhost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// newRequest fills in the context values Caddy would provide: the replacer that
// MatchHost requires, and the vars map holding trusted_proxy.
func newRequest(tb testing.TB, host string, xfh []string, trusted any) *http.Request {
	tb.Helper()

	r := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
	r.Host = host
	if xfh != nil {
		r.Header["X-Forwarded-Host"] = xfh
	}

	vars := map[string]any{}
	if trusted != nil {
		vars[caddyhttp.TrustedProxyVarKey] = trusted
	}
	ctx := context.WithValue(r.Context(), caddyhttp.VarsCtxKey, vars)
	ctx = context.WithValue(ctx, caddy.ReplacerCtxKey, caddy.NewReplacer())

	return r.WithContext(ctx)
}

func newMatcher(tb testing.TB, cfg MatchForwardedHost) MatchForwardedHost {
	tb.Helper()

	if err := cfg.Provision(caddy.Context{}); err != nil {
		tb.Fatalf("provision %v: %v", cfg.Hosts, err)
	}
	return cfg
}

func wantErrContains(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
}

// numberedHosts returns count hosts of the form h000.example.com, used to build
// lists on either side of MatchHost's 100-entry fast-path threshold.
func numberedHosts(count int) []string {
	hosts := make([]string, count)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("h%03d.example.com", i)
	}
	return hosts
}

func TestSelectForwardedHost(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
		state  forwardedState
	}{
		{name: "missing", values: nil, state: forwardedAbsent},
		{name: "empty field", values: []string{""}, state: forwardedAbsent},
		{name: "blank field", values: []string{" "}, state: forwardedAbsent},
		{name: "all blank fields", values: []string{"", "  "}, state: forwardedAbsent},
		{name: "character split across fields is not blank", values: []string{"\xc2", "\xa0"}, want: "\xa0", state: forwardedPicked},
		{name: "single value", values: []string{"app.example.com"}, want: "app.example.com", state: forwardedPicked},
		{name: "trims", values: []string{"  app.example.com \t"}, want: "app.example.com", state: forwardedPicked},
		{name: "rightmost of list", values: []string{"evil.example, app.example.com"}, want: "app.example.com", state: forwardedPicked},
		{name: "rightmost of list takes last", values: []string{"app.example.com, evil.example"}, want: "evil.example", state: forwardedPicked},
		{name: "last field wins", values: []string{"first.example.com", "second.example.com"}, want: "second.example.com", state: forwardedPicked},
		{name: "empty rightmost is invalid", values: []string{"app.example.com,"}, state: forwardedInvalid},
		{name: "comma only is invalid", values: []string{", "}, state: forwardedInvalid},
		{name: "empty last field is invalid", values: []string{"app.example.com", ""}, state: forwardedInvalid},
		{name: "port kept", values: []string{"app.example.com:8443"}, want: "app.example.com:8443", state: forwardedPicked},
		{name: "bracketed ipv6", values: []string{"[2001:db8::1]"}, want: "[2001:db8::1]", state: forwardedPicked},
		{name: "bracketed ipv6 with port", values: []string{"[2001:db8::1]:8443"}, want: "[2001:db8::1]:8443", state: forwardedPicked},
		{name: "bracketed ipv6 with junk suffix", values: []string{"[2001:db8::1]x"}, state: forwardedInvalid},
		{name: "bracketed ipv6 with empty port", values: []string{"[2001:db8::1]:"}, state: forwardedInvalid},
		{name: "unbracketed ipv6", values: []string{"2001:db8::1"}, state: forwardedInvalid},
		{name: "bracketed ipv4", values: []string{"[1.2.3.4]"}, state: forwardedInvalid},
		{name: "ipv6 zone", values: []string{"[fe80::1%eth0]"}, state: forwardedInvalid},
		{name: "unclosed bracket", values: []string{"[2001:db8::1"}, state: forwardedInvalid},
		{name: "stray bracket", values: []string{"a]b"}, state: forwardedInvalid},
		{name: "scheme", values: []string{"http://app.example.com"}, state: forwardedInvalid},
		{name: "userinfo", values: []string{"u@app.example.com"}, state: forwardedInvalid},
		{name: "space", values: []string{"a b"}, state: forwardedInvalid},
		{name: "empty host with port", values: []string{":80"}, state: forwardedInvalid},
		{name: "query", values: []string{"app.example.com?x=1"}, state: forwardedInvalid},
		{name: "fragment", values: []string{"app.example.com#x"}, state: forwardedInvalid},
		{name: "backslash", values: []string{"app.example.com\\evil"}, state: forwardedInvalid},
		{name: "control", values: []string{"app.example.com\x00"}, state: forwardedInvalid},
		{name: "header injection", values: []string{"app.example.com\r\nX-Evil: 1"}, state: forwardedInvalid},
		{name: "embedded tab", values: []string{"app.\texample.com"}, state: forwardedInvalid},
		{name: "newline only", values: []string{"\n"}, state: forwardedAbsent},
		{name: "wildcard is rejected", values: []string{"*.example.com"}, state: forwardedInvalid},
		{name: "non numeric port", values: []string{"app.example.com:abc"}, state: forwardedInvalid},
		{name: "port too long", values: []string{"app.example.com:123456"}, state: forwardedInvalid},
		{name: "port too large", values: []string{"app.example.com:70000"}, state: forwardedInvalid},
		{name: "port zero", values: []string{"app.example.com:0"}, state: forwardedInvalid},
		{name: "empty port", values: []string{"app.example.com:"}, state: forwardedInvalid},
		{name: "trailing dot is accepted", values: []string{"app.example.com."}, want: "app.example.com.", state: forwardedPicked},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, state := selectForwardedHost(tc.values)
			if state != tc.state {
				t.Fatalf("state = %v, want %v", state, tc.state)
			}
			if got != tc.want {
				t.Fatalf("host = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestValidPort only covers boundaries that are not reachable through
// TestSelectForwardedHost; the rest is covered end to end there.
func TestValidPort(t *testing.T) {
	for _, port := range []string{"1", "65535", "0080"} {
		if !validPort(port) {
			t.Errorf("validPort(%q) = false, want true", port)
		}
	}
	for _, port := range []string{"-1", "+80", "8_0", " 80", "80 ", "65536", "000080"} {
		if validPort(port) {
			t.Errorf("validPort(%q) = true, want false", port)
		}
	}
}

func TestValidateWildcardPlacement(t *testing.T) {
	for _, host := range []string{"app.example.com", "*.example.com", "a.*.example.com", "*", "{http.request.host}"} {
		if err := validateWildcardPlacement(host); err != nil {
			t.Errorf("validateWildcardPlacement(%q) = %v, want nil", host, err)
		}
	}
	for _, host := range []string{"*-app.example.com", "app*.example.com", "a.b*.example.com"} {
		if err := validateWildcardPlacement(host); err == nil {
			t.Errorf("validateWildcardPlacement(%q) = nil, want error", host)
		}
	}
}

func TestProvision(t *testing.T) {
	tests := []struct {
		name    string
		hosts   []string
		wantErr string
	}{
		{name: "empty list", hosts: nil, wantErr: "requires at least one host"},
		{name: "empty entry", hosts: []string{"app.example.com", ""}, wantErr: "empty host"},
		{name: "wildcard inside label", hosts: []string{"*-app.example.com"}, wantErr: "whole label"},
		{name: "duplicate host", hosts: []string{"app.example.com", "APP.example.com"}, wantErr: "repeated"},
		{name: "malformed punycode", hosts: []string{"xn--0.pt"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := MatchForwardedHost{Hosts: tc.hosts}
			err := m.Provision(caddy.Context{})
			if tc.wantErr == "" {
				// The message is upstream's and may be reworded; only the rejection is
				// this module's to pin.
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			wantErrContains(t, err, tc.wantErr)
		})
	}
}

func TestMatchWithError(t *testing.T) {
	tests := []struct {
		name    string
		hosts   []string
		require bool
		anyPeer bool
		host    string
		xfh     []string
		trusted any // nil means "variable absent"
		want    bool
		wantVar string
	}{
		// Default mode: fall back to the inbound Host.
		{name: "untrusted with xfh uses host", hosts: []string{"origin.internal"}, host: "origin.internal", xfh: []string{"app.example.com"}, trusted: false, want: true, wantVar: "origin.internal"},
		{name: "untrusted with xfh does not use xfh", hosts: []string{"app.example.com"}, host: "origin.internal", xfh: []string{"app.example.com"}, trusted: false, want: false},
		{name: "missing trusted var uses host", hosts: []string{"origin.internal"}, host: "origin.internal", xfh: []string{"app.example.com"}, want: true, wantVar: "origin.internal"},
		{name: "wrong typed trusted var uses host", hosts: []string{"origin.internal"}, host: "origin.internal", xfh: []string{"app.example.com"}, trusted: "true", want: true, wantVar: "origin.internal"},
		{name: "trusted without xfh uses host", hosts: []string{"origin.internal"}, host: "origin.internal", trusted: true, want: true, wantVar: "origin.internal"},
		{name: "fallback to host keeps its case", hosts: []string{"app.example.com"}, host: "APP.example.com", trusted: true, want: true, wantVar: "APP.example.com"},
		{name: "trusted with valid xfh", hosts: []string{"app.example.com"}, host: "origin.internal", xfh: []string{"app.example.com"}, trusted: true, want: true, wantVar: "app.example.com"},
		{name: "trusted picks rightmost", hosts: []string{"app.example.com"}, host: "origin.internal", xfh: []string{"evil.example, app.example.com"}, trusted: true, want: true, wantVar: "app.example.com"},
		{name: "trusted xfh not matching never falls back", hosts: []string{"origin.internal"}, host: "origin.internal", xfh: []string{"evil.example"}, trusted: true, want: false},
		{name: "trusted invalid xfh does not match", hosts: []string{"origin.internal"}, host: "origin.internal", xfh: []string{"a b"}, trusted: true, want: false},
		{name: "trusted invalid xfh with matching host", hosts: []string{"origin.internal"}, host: "origin.internal", xfh: []string{"http://origin.internal"}, trusted: true, want: false},
		{name: "wildcard matches subdomain", hosts: []string{"*.example.com"}, host: "origin.internal", xfh: []string{"app.example.com"}, trusted: true, want: true, wantVar: "app.example.com"},
		{name: "wildcard does not match two levels", hosts: []string{"*.example.com"}, host: "origin.internal", xfh: []string{"a.b.example.com"}, trusted: true, want: false},
		{name: "wildcard is not matched literally", hosts: []string{"*.example.com"}, host: "origin.internal", xfh: []string{"*.example.com"}, trusted: true, want: false},
		{name: "configured port never matches", hosts: []string{"app.example.com:8443"}, host: "origin.internal", xfh: []string{"app.example.com:8443"}, trusted: true, want: false},
		{name: "port is stripped for matching", hosts: []string{"app.example.com"}, host: "origin.internal", xfh: []string{"app.example.com:8443"}, trusted: true, want: true, wantVar: "app.example.com:8443"},
		{name: "case insensitive", hosts: []string{"app.example.com"}, host: "origin.internal", xfh: []string{"APP.Example.COM"}, trusted: true, want: true, wantVar: "APP.Example.COM"},

		// trust_any_peer: the server verdict is ignored, the header decides.
		{name: "any peer: untrusted valid xfh is used", hosts: []string{"app.example.com"}, anyPeer: true, host: "origin.internal", xfh: []string{"app.example.com"}, trusted: false, want: true, wantVar: "app.example.com"},
		{name: "any peer: missing trusted var uses xfh", hosts: []string{"app.example.com"}, anyPeer: true, host: "origin.internal", xfh: []string{"app.example.com"}, want: true, wantVar: "app.example.com"},
		{name: "any peer: rightmost item wins", hosts: []string{"app.example.com"}, anyPeer: true, host: "origin.internal", xfh: []string{"evil.example, app.example.com"}, trusted: false, want: true, wantVar: "app.example.com"},
		{name: "any peer: no xfh uses host", hosts: []string{"origin.internal"}, anyPeer: true, host: "origin.internal", trusted: false, want: true, wantVar: "origin.internal"},
		{name: "any peer: unmatched xfh never falls back", hosts: []string{"origin.internal"}, anyPeer: true, host: "origin.internal", xfh: []string{"evil.example"}, trusted: false, want: false},
		{name: "any peer: invalid xfh does not match", hosts: []string{"origin.internal"}, anyPeer: true, host: "origin.internal", xfh: []string{"a b"}, trusted: false, want: false},
		{name: "any peer: wildcard matches subdomain", hosts: []string{"*.example.com"}, anyPeer: true, host: "origin.internal", xfh: []string{"app.example.com"}, trusted: false, want: true, wantVar: "app.example.com"},

		// require_header
		{name: "require: untrusted rejected", hosts: []string{"origin.internal"}, require: true, host: "origin.internal", xfh: []string{"app.example.com"}, trusted: false, want: false},
		{name: "require: no xfh rejected", hosts: []string{"origin.internal"}, require: true, host: "origin.internal", trusted: true, want: false},
		{name: "require: trusted xfh accepted", hosts: []string{"app.example.com"}, require: true, host: "origin.internal", xfh: []string{"app.example.com"}, trusted: true, want: true, wantVar: "app.example.com"},
		{name: "require: invalid xfh rejected", hosts: []string{"origin.internal"}, require: true, host: "origin.internal", xfh: []string{"a b"}, trusted: true, want: false},
		{name: "require: all blank xfh rejected", hosts: []string{"origin.internal"}, require: true, host: "origin.internal", xfh: []string{" ", ""}, trusted: true, want: false},
		{name: "require + any peer: untrusted xfh accepted", hosts: []string{"app.example.com"}, require: true, anyPeer: true, host: "origin.internal", xfh: []string{"app.example.com"}, trusted: false, want: true, wantVar: "app.example.com"},
		{name: "require + any peer: missing xfh rejected", hosts: []string{"app.example.com"}, require: true, anyPeer: true, host: "origin.internal", trusted: false, want: false},
		{name: "require + any peer: invalid xfh rejected", hosts: []string{"app.example.com"}, require: true, anyPeer: true, host: "origin.internal", xfh: []string{"a b"}, trusted: false, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newMatcher(t, MatchForwardedHost{Hosts: tc.hosts, RequireHeader: tc.require, TrustAnyPeer: tc.anyPeer})
			r := newRequest(t, tc.host, tc.xfh, tc.trusted)

			got, err := m.MatchWithError(r)
			if err != nil {
				t.Fatalf("MatchWithError: %v", err)
			}
			if got != tc.want {
				t.Fatalf("match = %v, want %v", got, tc.want)
			}

			// Published only on a successful match, and it is the authority used.
			if tc.want {
				if v, ok := caddyhttp.GetVar(r.Context(), VarKey).(string); !ok || v != tc.wantVar {
					t.Fatalf("var %q = %v, want %q", VarKey, v, tc.wantVar)
				}
			} else if caddyhttp.GetVar(r.Context(), VarKey) != nil {
				t.Fatalf("var %q was set on a failed match", VarKey)
			}
		})
	}
}

func TestMatchAgreesWithMatchWithError(t *testing.T) {
	tests := []struct {
		name  string
		cfg   MatchForwardedHost
		host  string
		xfh   []string
		trust any
		want  bool
	}{
		{name: "trusted with valid xfh", cfg: MatchForwardedHost{Hosts: []string{"app.example.com"}}, host: "origin.internal", xfh: []string{"app.example.com"}, trust: true, want: true},
		{name: "untrusted falls back to host", cfg: MatchForwardedHost{Hosts: []string{"origin.internal"}}, host: "origin.internal", xfh: []string{"app.example.com"}, trust: false, want: true},
		{name: "trusted with invalid xfh", cfg: MatchForwardedHost{Hosts: []string{"origin.internal"}}, host: "origin.internal", xfh: []string{"a b"}, trust: true, want: false},
		{name: "require_header without header", cfg: MatchForwardedHost{Hosts: []string{"origin.internal"}, RequireHeader: true}, host: "origin.internal", trust: true, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newMatcher(t, tc.cfg)

			// Fresh requests for each call: matching publishes a variable, so the two
			// calls must start from the same request state.
			matched, err := m.MatchWithError(newRequest(t, tc.host, tc.xfh, tc.trust))
			if err != nil {
				t.Fatalf("MatchWithError: %v", err)
			}
			if got := m.Match(newRequest(t, tc.host, tc.xfh, tc.trust)); got != matched || got != tc.want {
				t.Fatalf("Match = %v, MatchWithError = %v, want %v", got, matched, tc.want)
			}
		})
	}
}

func TestMatchFailureKeepsPreviousVar(t *testing.T) {
	m := newMatcher(t, MatchForwardedHost{Hosts: []string{"app.example.com"}})
	r := newRequest(t, "origin.internal", []string{"evil.example"}, true)
	caddyhttp.SetVar(r.Context(), VarKey, "previous.example.com")

	matched, err := m.MatchWithError(r)
	if err != nil {
		t.Fatalf("MatchWithError: %v", err)
	}
	if matched {
		t.Fatal("match = true, want false")
	}
	if v := caddyhttp.GetVar(r.Context(), VarKey); v != "previous.example.com" {
		t.Fatalf("var = %v, want previous value kept", v)
	}
}

func TestMatchDoesNotMutateRequest(t *testing.T) {
	m := newMatcher(t, MatchForwardedHost{Hosts: []string{"app.example.com"}})
	r := newRequest(t, "origin.internal", []string{"app.example.com"}, true)
	headerBefore := r.Header.Clone()

	matched, err := m.MatchWithError(r)
	if err != nil || !matched {
		t.Fatalf("MatchWithError = %v, %v; want true, nil", matched, err)
	}
	if r.Host != "origin.internal" {
		t.Fatalf("r.Host = %q, want it unchanged", r.Host)
	}
	if len(r.Header) != len(headerBefore) {
		t.Fatalf("header map changed: %v", r.Header)
	}
	for k, v := range headerBefore {
		if !slices.Equal(r.Header[k], v) {
			t.Fatalf("header %s changed: %v, want %v", k, r.Header[k], v)
		}
	}
}

func TestModuleRegistration(t *testing.T) {
	info := MatchForwardedHost{}.CaddyModule()
	if info.ID != "http.matchers.forwarded_host" {
		t.Fatalf("module ID = %q", info.ID)
	}
	if _, err := caddy.GetModule("http.matchers.forwarded_host"); err != nil {
		t.Fatalf("module not registered: %v", err)
	}
}

func TestCaseInsensitiveLargeList(t *testing.T) {
	hosts := append(numberedHosts(100), "extra.example.com")
	upper := strings.ToUpper(hosts[0])

	// 100 entries do not use MatchHost's large-list fast path; 101 do.
	// Provision mutates its slice, so each case gets its own copy.
	for _, count := range []int{100, 101} {
		m := newMatcher(t, MatchForwardedHost{Hosts: slices.Clone(hosts[:count])})

		trusted := newRequest(t, "origin.internal", []string{upper}, true)
		if ok, err := m.MatchWithError(trusted); err != nil || !ok {
			t.Errorf("%d hosts, trusted %s: match = %v, %v; want true", count, upper, ok, err)
		}

		direct := newRequest(t, upper, nil, nil)
		if ok, err := m.MatchWithError(direct); err != nil || !ok {
			t.Errorf("%d hosts, direct %s: match = %v, %v; want true", count, upper, ok, err)
		}
	}
}

func TestNotWrapperStillPublishesVar(t *testing.T) {
	m := newMatcher(t, MatchForwardedHost{Hosts: []string{"app.example.com"}})
	r := newRequest(t, "origin.internal", []string{"app.example.com"}, true)

	not := caddyhttp.MatchNot{MatcherSets: []caddyhttp.MatcherSet{{m}}}
	matched, err := not.MatchWithError(r)
	if err != nil {
		t.Fatalf("MatchWithError: %v", err)
	}
	if matched {
		t.Fatal("not(...) match = true, want false")
	}
	// The inner matcher still ran, so its output is visible downstream.
	if v := caddyhttp.GetVar(r.Context(), VarKey); v != "app.example.com" {
		t.Fatalf("var = %v, want the inner match to have published it", v)
	}
}

func TestEmptyAuthorityNotPublished(t *testing.T) {
	// An empty placeholder entry can match a request without a Host (HTTP/1.0);
	// nothing usable took part in matching, so nothing may be published.
	m := newMatcher(t, MatchForwardedHost{Hosts: []string{"{env.FORWARDED_HOST_UNSET}"}})
	r := newRequest(t, "origin.internal", nil, nil)
	r.Host = ""

	matched, err := m.MatchWithError(r)
	if err != nil {
		t.Fatalf("MatchWithError: %v", err)
	}
	if !matched {
		t.Fatal("match = false, want true (empty entry equals empty host)")
	}
	if v := caddyhttp.GetVar(r.Context(), VarKey); v != nil {
		t.Fatalf("var = %v, want it not to be published", v)
	}
}

func TestTrustAnyPeerDoesNotWriteVars(t *testing.T) {
	// client_ip and reverse_proxy read these variables, so the bypass has to stay
	// local: whatever the server computed must survive the match unchanged.
	for _, tc := range []struct {
		name    string
		trusted any
	}{
		{name: "trusted false", trusted: false},
		{name: "trusted true", trusted: true},
		{name: "variable absent", trusted: nil},
		{name: "wrong type", trusted: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMatcher(t, MatchForwardedHost{Hosts: []string{"app.example.com"}, TrustAnyPeer: true})
			r := newRequest(t, "origin.internal", []string{"app.example.com"}, tc.trusted)
			caddyhttp.SetVar(r.Context(), caddyhttp.ClientIPVarKey, "203.0.113.9")

			if ok, err := m.MatchWithError(r); err != nil || !ok {
				t.Fatalf("MatchWithError = %v, %v; want true, nil", ok, err)
			}
			if trusted := caddyhttp.GetVar(r.Context(), caddyhttp.TrustedProxyVarKey); trusted != tc.trusted {
				t.Fatalf("%s = %v, want it unchanged (%v)", caddyhttp.TrustedProxyVarKey, trusted, tc.trusted)
			}
			if ip := caddyhttp.GetVar(r.Context(), caddyhttp.ClientIPVarKey); ip != "203.0.113.9" {
				t.Fatalf("%s = %v, want it unchanged", caddyhttp.ClientIPVarKey, ip)
			}
		})
	}
}

func TestLargeListMatchesHostDelegation(t *testing.T) {
	// The matcher must agree with a bare caddyhttp.MatchHost given the same
	// lowercased request host. What that does with an uppercase exact entry differs
	// by dependency version (v2.10.2 keeps entries as written, v2.11.9 normalizes
	// them), so the reference is provisioned independently instead of being inferred
	// from our own provisioned field.
	hosts := append(numberedHosts(100), "Mixed.Example.COM")

	reference := caddyhttp.MatchHost(slices.Clone(hosts))
	if err := reference.Provision(caddy.Context{}); err != nil {
		t.Fatalf("provision reference: %v", err)
	}
	m := newMatcher(t, MatchForwardedHost{Hosts: hosts})

	for _, xfh := range []string{"Mixed.Example.COM", "mixed.example.com"} {
		want, err := reference.MatchWithError(newRequest(t, strings.ToLower(xfh), nil, true))
		if err != nil {
			t.Fatalf("reference match %s: %v", xfh, err)
		}

		r := newRequest(t, "origin.internal", []string{xfh}, true)
		got, err := m.MatchWithError(r)
		if err != nil {
			t.Fatalf("match %s: %v", xfh, err)
		}
		if got != want {
			t.Fatalf("%s: match = %v, want %v (delegated MatchHost)", xfh, got, want)
		}
		if want {
			if v := caddyhttp.GetVar(r.Context(), VarKey); v != xfh {
				t.Fatalf("%s: var = %v, want the authority in its original case", xfh, v)
			}
		}
	}
}
