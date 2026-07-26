package config_test

import (
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/platform/config"
)

func TestProviderConfig_Mode(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.ProviderConfig
		want config.FederationMode
	}{
		{
			name: "all empty resolves standalone",
			cfg:  config.ProviderConfig{},
			want: config.FederationModeStandalone,
		},
		{
			name: "all set resolves federated",
			cfg: config.ProviderConfig{
				BaseURL: "https://nestova.example", ClientID: "client-1", ClientSecret: "shh",
			},
			want: config.FederationModeFederated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Mode(); got != tt.want {
				t.Errorf("Mode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProviderConfig_Validate(t *testing.T) {
	tests := []struct {
		name       string
		cfg        config.ProviderConfig
		wantNames  []string // substrings every returned error must collectively include
		wantErrLen int      // exact number of errors expected, so a partial fix isn't silently under-reported
	}{
		{
			name:       "all empty is valid (standalone)",
			cfg:        config.ProviderConfig{},
			wantErrLen: 0,
		},
		{
			name: "all set with a valid origin is valid (federated)",
			cfg: config.ProviderConfig{
				BaseURL: "https://nestova.example", ClientID: "client-1", ClientSecret: "shh",
			},
			wantErrLen: 0,
		},
		{
			name:       "only base URL set",
			cfg:        config.ProviderConfig{BaseURL: "https://nestova.example"},
			wantNames:  []string{"PROVIDER_CLIENT_ID", "PROVIDER_CLIENT_SECRET"},
			wantErrLen: 2,
		},
		{
			name:       "id without secret (base URL also missing)",
			cfg:        config.ProviderConfig{ClientID: "client-1"},
			wantNames:  []string{"PROVIDER_BASE_URL", "PROVIDER_CLIENT_SECRET"},
			wantErrLen: 2,
		},
		{
			name:       "secret without id (base URL also missing)",
			cfg:        config.ProviderConfig{ClientSecret: "shh"},
			wantNames:  []string{"PROVIDER_BASE_URL", "PROVIDER_CLIENT_ID"},
			wantErrLen: 2,
		},
		{
			name: "id and secret set, base URL missing",
			cfg: config.ProviderConfig{
				ClientID: "client-1", ClientSecret: "shh",
			},
			wantNames:  []string{"PROVIDER_BASE_URL"},
			wantErrLen: 1,
		},
		{
			name: "malformed base URL rejected",
			cfg: config.ProviderConfig{
				BaseURL: "://not-a-url", ClientID: "client-1", ClientSecret: "shh",
			},
			wantNames:  []string{"PROVIDER_BASE_URL"},
			wantErrLen: 1,
		},
		{
			name: "relative base URL rejected",
			cfg: config.ProviderConfig{
				BaseURL: "nestova.example", ClientID: "client-1", ClientSecret: "shh",
			},
			wantNames:  []string{"PROVIDER_BASE_URL"},
			wantErrLen: 1,
		},
		{
			name: "path-bearing base URL rejected",
			cfg: config.ProviderConfig{
				BaseURL: "https://nestova.example/oauth", ClientID: "client-1", ClientSecret: "shh",
			},
			wantNames:  []string{"PROVIDER_BASE_URL"},
			wantErrLen: 1,
		},
		{
			name: "base URL with userinfo rejected",
			cfg: config.ProviderConfig{
				BaseURL: "https://user:pass@nestova.example", ClientID: "client-1", ClientSecret: "shh",
			},
			wantNames:  []string{"PROVIDER_BASE_URL"},
			wantErrLen: 1,
		},
		{
			name: "non-http(s) scheme rejected",
			cfg: config.ProviderConfig{
				BaseURL: "ftp://nestova.example", ClientID: "client-1", ClientSecret: "shh",
			},
			wantNames:  []string{"PROVIDER_BASE_URL"},
			wantErrLen: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.cfg.Validate()
			if len(errs) != tt.wantErrLen {
				t.Fatalf("Validate() returned %d errors %v, want %d", len(errs), errs, tt.wantErrLen)
			}
			joined := errsToString(errs)
			for _, name := range tt.wantNames {
				if !strings.Contains(joined, name) {
					t.Errorf("Validate() errors = %q, want it to name %s", joined, name)
				}
			}
		})
	}
}

func TestProviderConfig_Host(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.ProviderConfig
		want string
	}{
		{
			name: "standalone (empty BaseURL) has no host",
			cfg:  config.ProviderConfig{},
			want: "",
		},
		{
			name: "federated BaseURL yields its host",
			cfg:  config.ProviderConfig{BaseURL: "https://nestova.example:8443"},
			want: "nestova.example:8443",
		},
		{
			name: "malformed BaseURL yields empty rather than panicking",
			cfg:  config.ProviderConfig{BaseURL: "://not-a-url"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Host(); got != tt.want {
				t.Errorf("Host() = %q, want %q", got, tt.want)
			}
		})
	}
}

func errsToString(errs []error) string {
	var b strings.Builder
	for _, e := range errs {
		b.WriteString(e.Error())
		b.WriteString("; ")
	}
	return b.String()
}
