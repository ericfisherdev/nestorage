package adapter

import "testing"

// TestSanitizeNext covers the post-login redirect sanitizer's same-origin
// guard directly (white-box: sanitizeNext is unexported). Ported from
// Nestova's own auth handler test, which the ticket's implementation plan
// names as the source for this exact logic.
func TestSanitizeNext(t *testing.T) {
	tests := []struct {
		name string
		next string
		want string
	}{
		{"empty defaults to root", "", "/"},
		{"simple path", "/bins", "/bins"},
		{"path with query string", "/bins?owner=maya", "/bins?owner=maya"},
		{"absolute URL is rejected", "https://evil.example/steal", "/"},
		{"protocol-relative URL is rejected", "//evil.example/steal", "/"},
		{"missing leading slash is rejected", "evil.example", "/"},
		{"ordinary traversal is cleaned, not rejected", "/foo/../bar", "/bar"},
		{"traversal past root collapses to a same-origin path, not rejected", "/foo/..//evil.com", "/evil.com"},
		{"malformed percent-encoding falls back to root", "/%zz", "/"},
		{
			// Regression: browsers normalize a backslash to a forward slash
			// when resolving a URL, so this exact string — which path.Clean
			// leaves completely untouched, since it only treats '/' as a
			// separator — would otherwise reach http.Redirect verbatim and
			// then be followed by the browser as the protocol-relative
			// "//evil.example/steal", an off-origin redirect.
			name: `literal backslash is rejected (browser \ -> / normalization)`,
			next: `/\evil.example/steal`,
			want: "/",
		},
		{
			name: "percent-encoded backslash is rejected",
			next: "/%5Cevil.example/steal",
			want: "/",
		},
		{
			name: `leading backslash-slash is rejected`,
			next: `/\/evil`,
			want: "/",
		},
		{
			name: `backslash embedded mid-path is rejected`,
			next: `/foo/\..\evil`,
			want: "/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeNext(tt.next); got != tt.want {
				t.Errorf("sanitizeNext(%q) = %q, want %q", tt.next, got, tt.want)
			}
		})
	}
}
