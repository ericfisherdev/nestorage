package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// verifySearchPathTimeout bounds TestVerifySearchPath_UnreachableDSNFailsFast's
// query against an unreachable address — an unbounded context.Background()
// would otherwise wait out the OS's own TCP connect timeout rather than
// failing fast, mirroring cmd/server's identical
// TestVerifyOwnSchema_MockPoolError rationale.
const verifySearchPathTimeout = 5 * time.Second

// TestVerifySearchPath_UnreachableDSNFailsFast confirms verifySearchPath
// wraps a connection failure rather than swallowing it, without needing a
// live database.
func TestVerifySearchPath_UnreachableDSNFailsFast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), verifySearchPathTimeout)
	defer cancel()

	err := verifySearchPath(ctx, "postgres://u:p@127.0.0.1:1/nope", "nestorage")
	if err == nil {
		t.Fatal("verifySearchPath() over an unreachable address = nil error, want error")
	}
	if !strings.Contains(err.Error(), "search_path") {
		t.Errorf("error = %q, want it to name the search_path check", err.Error())
	}
}
