// Package components holds Nestorage's Go Templ view components: the
// Hearth app shell (layout.templ) and the presentation-layer pieces it and
// later features compose from — icons, owner avatars, filters, and the rest
// of the shared partials under this package.
//
// This doc comment lives on aria.go, the package's one hand-written .go
// file: every other source file is templ-generated, and golangci-lint's
// exclusions.generated:strict setting skips generated files entirely, so a
// comment placed in a .templ file's output would never be seen by the
// package-comments lint rule.
package components

// ariaBool renders a Go bool as the string an aria-* attribute expects
// ("true"/"false"); templ attribute expressions require a string.
func ariaBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
