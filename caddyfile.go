package forwardedhost

import (
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
)

// UnmarshalCaddyfile implements caddyfile.Unmarshaler.
//
//	forwarded_host <host...> {
//		require_header
//		trust_any_peer
//	}
//
// Hosts must be on the matcher's own line. Inside one named-matcher block the
// module may repeat on separate lines (Caddy concatenates them into one token
// stream); defining the same @name twice is an error.
func (m *MatchForwardedHost) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		m.Hosts = append(m.Hosts, d.RemainingArgs()...)

		nesting := d.Nesting()
		for d.NextBlock(nesting) {
			name := d.Val()
			var err error
			switch name {
			case "require_header":
				err = setBoolOption(d, name, &m.RequireHeader)
			case "trust_any_peer":
				err = setBoolOption(d, name, &m.TrustAnyPeer)
			default:
				return d.Errf("unrecognized subdirective '%s'", name)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// setBoolOption enables a valueless boolean option, rejecting arguments and duplicates.
func setBoolOption(d *caddyfile.Dispenser, name string, flag *bool) error {
	if d.NextArg() {
		return d.ArgErr()
	}
	if *flag {
		return d.Errf("%s specified more than once", name)
	}
	*flag = true
	return nil
}
