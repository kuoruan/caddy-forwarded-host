package forwardedhost

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2/caddytest"
)

// TestIntegration runs a real Caddy in-process, so prepareRequest computes
// trusted_proxy before routing. Dialing is redirected to 127.0.0.1, which is what
// makes the peer trusted on :9081.
func TestIntegration(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "host=%s xff=%s", r.Host, r.Header.Get("X-Forwarded-For"))
	}))
	defer backend.Close()

	backendAddr := backend.Listener.Addr().String()

	// trusted_proxies is configured for :9081 only, so the same request on
	// :9082 must fall back to the inbound Host. :9083 has no trusted_proxies
	// either, but its matcher sets trust_any_peer, so the header counts there.
	config := fmt.Sprintf(`
	{
		admin localhost:2999
		servers :9081 {
			trusted_proxies static 127.0.0.1/32
		}
	}

	:9083 {
		@apps forwarded_host *.example.com {
			trust_any_peer
		}
		handle @apps {
			reverse_proxy %s {
				header_up Host {http.vars.forwarded_host}
			}
		}
		handle {
			respond "not found" 404
		}
	}

	:9081, :9082 {
		@apps forwarded_host *.example.com
		handle @apps {
			reverse_proxy %s {
				header_up Host {http.vars.forwarded_host}
			}
		}
		handle {
			respond "not found" 404
		}
	}
	`, backendAddr, backendAddr)

	tester := caddytest.NewTester(t)
	tester.InitServer(config, "caddyfile")

	tests := []struct {
		name       string
		addr       string
		host       string
		xfh        string
		xff        string
		wantStatus int
		wantBody   string
		notBody    string
	}{
		{
			name:       "trusted proxy routes on X-Forwarded-Host and forwards it upstream",
			addr:       ":9081",
			host:       "origin.internal",
			xfh:        "app.example.com",
			wantStatus: http.StatusOK,
			wantBody:   "host=app.example.com",
		},
		{
			name:       "trusted proxy keeps the port upstream",
			addr:       ":9081",
			host:       "origin.internal",
			xfh:        "app.example.com:8443",
			wantStatus: http.StatusOK,
			wantBody:   "host=app.example.com:8443",
		},
		{
			name:       "trusted proxy with unmatched X-Forwarded-Host does not fall back",
			addr:       ":9081",
			host:       "origin.internal",
			xfh:        "evil.example",
			wantStatus: http.StatusNotFound,
			wantBody:   "not found",
		},
		{
			// The inbound Host would match, so a 404 here proves there is no fallback.
			name:       "trusted proxy with invalid X-Forwarded-Host is rejected",
			addr:       ":9081",
			host:       "app.example.com",
			xfh:        "a b",
			wantStatus: http.StatusNotFound,
			wantBody:   "not found",
		},
		{
			name:       "direct request to the public host still matches",
			addr:       ":9081",
			host:       "app.example.com",
			wantStatus: http.StatusOK,
			wantBody:   "host=app.example.com",
		},
		{
			name:       "untrusted peer falls back to the inbound host",
			addr:       ":9082",
			host:       "app.example.com",
			wantStatus: http.StatusOK,
			wantBody:   "host=app.example.com",
		},
		{
			name:       "trust_any_peer uses X-Forwarded-Host from an untrusted peer",
			addr:       ":9083",
			host:       "origin.internal",
			xfh:        "app.example.com",
			wantStatus: http.StatusOK,
			wantBody:   "host=app.example.com",
		},
		{
			name:       "trust_any_peer without X-Forwarded-Host falls back to the inbound host",
			addr:       ":9083",
			host:       "app.example.com",
			wantStatus: http.StatusOK,
			wantBody:   "host=app.example.com",
		},
		{
			// The inbound Host would match, so a 404 here proves there is no fallback.
			name:       "trust_any_peer with invalid X-Forwarded-Host is rejected",
			addr:       ":9083",
			host:       "app.example.com",
			xfh:        "a b",
			wantStatus: http.StatusNotFound,
			wantBody:   "not found",
		},
		{
			// The matcher trusts the peer here, but the server verdict still governs
			// what reverse_proxy does with a client-supplied X-Forwarded-For.
			name:       "trust_any_peer does not make reverse_proxy trust X-Forwarded-For",
			addr:       ":9083",
			host:       "origin.internal",
			xfh:        "app.example.com",
			xff:        "1.2.3.4",
			wantStatus: http.StatusOK,
			wantBody:   "xff=127.0.0.1",
			notBody:    "1.2.3.4",
		},
		{
			name:       "untrusted peer cannot use X-Forwarded-Host",
			addr:       ":9082",
			host:       "origin.internal",
			xfh:        "app.example.com",
			wantStatus: http.StatusNotFound,
			wantBody:   "not found",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "http://origin.internal"+test.addr+"/", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Host = test.host
			if test.xfh != "" {
				req.Header.Set("X-Forwarded-Host", test.xfh)
			}
			if test.xff != "" {
				req.Header.Set("X-Forwarded-For", test.xff)
			}

			resp, err := tester.Client.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()

			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			body := string(raw)

			if resp.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", resp.StatusCode, test.wantStatus, body)
			}
			if !strings.Contains(body, test.wantBody) {
				t.Fatalf("body = %q, want it to contain %q", body, test.wantBody)
			}
			if test.notBody != "" && strings.Contains(body, test.notBody) {
				t.Fatalf("body = %q, want it not to contain %q", body, test.notBody)
			}
		})
	}
}
