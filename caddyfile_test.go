package forwardedhost

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
)

func TestUnmarshalCaddyfile(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string // JSON of the resulting config
		wantErr string
	}{
		{
			name:  "single line",
			input: "forwarded_host app.example.com app2.example.com",
			want:  `{"hosts":["app.example.com","app2.example.com"]}`,
		},
		{
			name:  "block with require_header",
			input: "forwarded_host app.example.com {\n\trequire_header\n}",
			want:  `{"hosts":["app.example.com"],"require_header":true}`,
		},
		{
			name:  "block with trust_any_peer",
			input: "forwarded_host app.example.com {\n\ttrust_any_peer\n}",
			want:  `{"hosts":["app.example.com"],"trust_any_peer":true}`,
		},
		{
			name:  "block with both flags",
			input: "forwarded_host app.example.com {\n\trequire_header\n\ttrust_any_peer\n}",
			want:  `{"hosts":["app.example.com"],"require_header":true,"trust_any_peer":true}`,
		},
		{
			name:  "two instances on separate lines merge",
			input: "forwarded_host app.example.com\nforwarded_host app2.example.com",
			want:  `{"hosts":["app.example.com","app2.example.com"]}`,
		},
		{
			name:  "second instance carries the flag",
			input: "forwarded_host app.example.com {\n\trequire_header\n}\nforwarded_host app2.example.com",
			want:  `{"hosts":["app.example.com","app2.example.com"],"require_header":true}`,
		},
		{
			// Known upstream behaviour: on one line the module name is parsed as a
			// host. Pinned, not corrected.
			name:  "same line repeat is not a merge",
			input: "forwarded_host app.example.com forwarded_host app2.example.com",
			want:  `{"hosts":["app.example.com","forwarded_host","app2.example.com"]}`,
		},
		{
			name:    "hosts on their own line",
			input:   "forwarded_host app.example.com {\n\tapp2.example.com\n}",
			wantErr: "unrecognized subdirective 'app2.example.com'",
		},
		{
			name:    "require_header with argument",
			input:   "forwarded_host app.example.com {\n\trequire_header true\n}",
			wantErr: "wrong argument count",
		},
		{
			name:    "require_header twice",
			input:   "forwarded_host app.example.com {\n\trequire_header\n\trequire_header\n}",
			wantErr: "more than once",
		},
		{
			name:    "trust_any_peer with argument",
			input:   "forwarded_host app.example.com {\n\ttrust_any_peer true\n}",
			wantErr: "wrong argument count",
		},
		{
			name:    "trust_any_peer twice",
			input:   "forwarded_host app.example.com {\n\ttrust_any_peer\n\ttrust_any_peer\n}",
			wantErr: "more than once",
		},
		{
			name:    "unknown subdirective",
			input:   "forwarded_host app.example.com {\n\ttrusted_proxies 10.0.0.0/8\n}",
			wantErr: "unrecognized subdirective 'trusted_proxies'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := caddyfile.NewTestDispenser(tc.input)
			m := MatchForwardedHost{}
			err := m.UnmarshalCaddyfile(d)

			if tc.wantErr != "" {
				wantErrContains(t, err, tc.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got, err := json.Marshal(m)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("config = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestCaddyfileBlockMerge runs the real adapter, which is what proves that
// repeated matcher lines in one block reach UnmarshalCaddyfile as one stream.
func TestCaddyfileBlockMerge(t *testing.T) {
	out, err := adaptCaddyfile(t, `:8080 {
	@x {
		forwarded_host a.example.com
		forwarded_host b.example.com {
			require_header
		}
	}
	handle @x {
		respond "ok"
	}
}
`)
	if err != nil {
		t.Fatalf("adapt: %v", err)
	}
	if !strings.Contains(string(out), `"hosts":["a.example.com","b.example.com"],"require_header":true`) {
		t.Fatalf("adapted config = %s", out)
	}
}

// TestCaddyfileDuplicateNamedMatcher pins upstream behavior: defining the same
// @name twice is an error, so repeating only works inside one block.
func TestCaddyfileDuplicateNamedMatcher(t *testing.T) {
	_, err := adaptCaddyfile(t, `:8080 {
	@x forwarded_host a.example.com
	@x forwarded_host b.example.com
	handle @x {
		respond "ok"
	}
}
`)
	if err == nil || !strings.Contains(err.Error(), "defined more than once") {
		t.Fatalf("adapt error = %v, want a duplicate-matcher error", err)
	}
}

func adaptCaddyfile(t *testing.T, cfg string) ([]byte, error) {
	t.Helper()
	a := caddyfile.Adapter{ServerType: httpcaddyfile.ServerType{}}
	out, _, err := a.Adapt([]byte(cfg), nil)
	return out, err
}
