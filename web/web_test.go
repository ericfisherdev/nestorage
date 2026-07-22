package web_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ericfisherdev/nestorage/web"
)

// TestStaticFSResolvesEmbeddedAssets verifies StaticFS is rooted so paths
// resolve without the leading "static/" — the mistake fs.Sub exists to
// prevent — and that every asset the shell references (the built CSS
// bundle, both vendored scripts, and both self-hosted fonts) actually
// shipped. A missing font here is a font nobody ever committed.
func TestStaticFSResolvesEmbeddedAssets(t *testing.T) {
	for _, path := range []string{
		"css/app.css",
		"js/htmx.min.js",
		"js/alpine.min.js",
		"fonts/hanken-grotesk.woff2",
		"fonts/space-mono.woff2",
		"favicon.svg",
	} {
		data, err := fs.ReadFile(web.StaticFS(), path)
		if err != nil {
			t.Errorf("read embedded %s: %v", path, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("embedded %s is empty", path)
		}
	}
}

// TestStaticHandlerServesAssets verifies the embedded assets are actually
// reachable over HTTP through StaticHandler, not just present in the fs.FS.
func TestStaticHandlerServesAssets(t *testing.T) {
	srv := httptest.NewServer(http.StripPrefix("/static/", web.StaticHandler()))
	t.Cleanup(srv.Close)

	for _, path := range []string{
		"/static/css/app.css",
		"/static/js/htmx.min.js",
		"/static/js/alpine.min.js",
		"/static/fonts/hanken-grotesk.woff2",
		"/static/fonts/space-mono.woff2",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Errorf("GET %s: %v", path, err)
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want %d", path, resp.StatusCode, http.StatusOK)
		}
	}
}
