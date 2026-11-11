package forwardedhost

import (
	"slices"
	"testing"
)

// runBench measures one matching scenario with a single shared request, which is
// what Caddy does: a request is matched once and never mutated by this module.
func runBench(b *testing.B, m MatchForwardedHost, host string, xfh []string, trusted any, want bool) {
	b.Helper()

	matcher := newMatcher(b, m)
	r := newRequest(b, host, xfh, trusted)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := matcher.MatchWithError(r)
		if err != nil || got != want {
			b.Fatalf("match = %v, %v; want %v", got, err, want)
		}
	}
}

func BenchmarkMatch(b *testing.B) {
	b.Run("PickedSmallList", func(b *testing.B) {
		runBench(b, MatchForwardedHost{Hosts: []string{"app.example.com"}}, "origin.internal", []string{"app.example.com"}, true, true)
	})
	b.Run("PickedUppercaseValue", func(b *testing.B) {
		runBench(b, MatchForwardedHost{Hosts: []string{"app.example.com"}}, "origin.internal", []string{"APP.Example.COM"}, true, true)
	})
	b.Run("PickedRightmostItem", func(b *testing.B) {
		runBench(b, MatchForwardedHost{Hosts: []string{"app.example.com"}}, "origin.internal", []string{"edge.example, app.example.com"}, true, true)
	})
	b.Run("Direct", func(b *testing.B) {
		runBench(b, MatchForwardedHost{Hosts: []string{"app.example.com"}}, "app.example.com", nil, true, true)
	})
	b.Run("UntrustedFallsBack", func(b *testing.B) {
		runBench(b, MatchForwardedHost{Hosts: []string{"app.example.com"}}, "app.example.com", []string{"app.example.com"}, false, true)
	})
	b.Run("InvalidValue", func(b *testing.B) {
		runBench(b, MatchForwardedHost{Hosts: []string{"app.example.com"}}, "origin.internal", []string{"a b"}, true, false)
	})
	b.Run("TrustAnyPeer", func(b *testing.B) {
		runBench(b, MatchForwardedHost{Hosts: []string{"app.example.com"}, TrustAnyPeer: true}, "origin.internal", []string{"app.example.com"}, false, true)
	})
	b.Run("Wildcard", func(b *testing.B) {
		runBench(b, MatchForwardedHost{Hosts: []string{"*.example.com"}}, "origin.internal", []string{"app.example.com"}, true, true)
	})
}

func BenchmarkMatchLargeList(b *testing.B) {
	const count = 1000
	hosts := numberedHosts(count)
	// Provision normalizes and sorts the list in place, so each scenario gets its own
	// copy. The target host is read from the untouched template, so the sort cannot
	// change which entry is targeted.
	target := hosts[len(hosts)-1]

	b.Run("Picked", func(b *testing.B) {
		runBench(b, MatchForwardedHost{Hosts: slices.Clone(hosts)}, "origin.internal", []string{target}, true, true)
	})
	b.Run("Direct", func(b *testing.B) {
		runBench(b, MatchForwardedHost{Hosts: slices.Clone(hosts)}, target, nil, true, true)
	})
	b.Run("NoMatch", func(b *testing.B) {
		runBench(b, MatchForwardedHost{Hosts: slices.Clone(hosts)}, "origin.internal", []string{"absent.example.com"}, true, false)
	})
}

func BenchmarkUpstreamMatchHostBaseline(b *testing.B) {
	// What this module delegates to, without our trust check, header parsing or
	// request copy. The gap to BenchmarkMatch is what this module adds.
	m := newMatcher(b, MatchForwardedHost{Hosts: []string{"app.example.com"}})
	r := newRequest(b, "app.example.com", nil, true)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ok, err := m.hosts.MatchWithError(r); err != nil || !ok {
			b.Fatalf("match = %v, %v", ok, err)
		}
	}
}

func BenchmarkSelectForwardedHost(b *testing.B) {
	cases := map[string][]string{
		"Single":       {"app.example.com"},
		"CommaList":    {"edge.example, app.example.com"},
		"Uppercase":    {"APP.Example.COM"},
		"WithPort":     {"app.example.com:8443"},
		"IPv6Bracket":  {"[2001:db8::1]:8443"},
		"InvalidValue": {"a b"},
	}
	for name, values := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				selectForwardedHost(values)
			}
		})
	}
}
