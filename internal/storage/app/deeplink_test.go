package app_test

import (
	"testing"

	"github.com/ericfisherdev/nestorage/internal/storage/app"
)

func TestBinDeepLinkURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		code string
		want string
	}{
		{
			name: "normalizes case and trims whitespace",
			base: "https://nestorage.example.ts.net",
			code: "  pantry-1  ",
			want: "https://nestorage.example.ts.net/b/PANTRY-1",
		},
		{
			name: "escapes a space in the code",
			base: "https://nestorage.example.ts.net",
			code: "attic box",
			want: "https://nestorage.example.ts.net/b/ATTIC%20BOX",
		},
		{
			name: "escapes a slash in the code",
			base: "https://nestorage.example.ts.net",
			code: "a/b",
			want: "https://nestorage.example.ts.net/b/A%2FB",
		},
		{
			name: "escapes a multi-byte rune in the code",
			base: "https://nestorage.example.ts.net",
			code: "büro",
			want: "https://nestorage.example.ts.net/b/B%C3%9CRO",
		},
		{
			name: "no double slash between base and the path",
			base: "https://nestorage.example.ts.net",
			code: "hall1",
			want: "https://nestorage.example.ts.net/b/HALL1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := app.BinDeepLinkURL(tt.base, tt.code)
			if got != tt.want {
				t.Errorf("BinDeepLinkURL(%q, %q) = %q, want %q", tt.base, tt.code, got, tt.want)
			}
		})
	}
}
