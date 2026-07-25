package app_test

import (
	"context"

	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// fakeEventAppender is an in-memory app.EventAppender fake shared by
// operations_test.go, mover_test.go, and item_test.go: Append records a
// copy of every event it successfully appends, or fails with appendErr
// (recording nothing) when set — the failing half of the
// TestXxxEventFailureAbortsXxx tests each of those three files defines.
type fakeEventAppender struct {
	events    []domain.ItemEvent
	appendErr error
}

func (f *fakeEventAppender) Append(_ context.Context, e *domain.ItemEvent) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	f.events = append(f.events, *e)
	return nil
}
