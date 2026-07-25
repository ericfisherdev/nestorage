package adapter

// This file is package adapter (white-box), not adapter_test — the same
// narrow exception item_history_internal_test.go documents (see its own
// doc), needed here because resolveBaseURL is unexported and the "forwarded
// scheme honored"/"TLS fallback" cases require driving nestcore's
// middleware.ForwardedHeaders and a synthetic *tls.ConnectionState directly,
// neither of which an HTTP-level black-box test in this package can reach.

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"
)

func TestResolveBaseURL(t *testing.T) {
	tests := []struct {
		name           string
		publicBaseURL  string
		useTLS         bool
		forwardedProto string // set only for the "forwarded scheme honored" case
		want           string
	}{
		{
			name:          "configured public base URL wins",
			publicBaseURL: "https://nestorage.tailnet-name.ts.net",
			// A configured PUBLIC_BASE_URL must win even when the request
			// itself looks like plain HTTP with no forwarded headers — the
			// whole point is staying correct for a scanner off the LAN.
			want: "https://nestorage.tailnet-name.ts.net",
		},
		{
			name:           "forwarded scheme honored",
			forwardedProto: "https",
			want:           "https://example.test",
		},
		{
			name:   "TLS fallback when no forwarded middleware ran",
			useTLS: true,
			want:   "https://example.test",
		},
		{
			name: "plain HTTP fallback",
			want: "http://example.test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/b/CODE1", nil)
			req.Host = "example.test"
			if tt.useTLS {
				req.TLS = &tls.ConnectionState{}
			}

			var got string
			handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = resolveBaseURL(r, tt.publicBaseURL)
			})

			if tt.forwardedProto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.forwardedProto)
				req.RemoteAddr = "127.0.0.1:12345"
				trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
				middleware.ForwardedHeaders(trusted)(handler).ServeHTTP(httptest.NewRecorder(), req)
			} else {
				handler.ServeHTTP(httptest.NewRecorder(), req)
			}

			if got != tt.want {
				t.Errorf("resolveBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
