package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

func TestRevokers_RevokeAll_FansOutToEveryRevoker(t *testing.T) {
	t.Parallel()
	id := domain.NewUserID()
	a := &fakeRevoker{}
	b := &fakeRevoker{}
	revokers := app.Revokers{a, b}

	if err := revokers.RevokeAll(context.Background(), id); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	if len(a.calls) != 1 || a.calls[0] != id {
		t.Errorf("first revoker calls = %v, want exactly one call for %v", a.calls, id)
	}
	if len(b.calls) != 1 || b.calls[0] != id {
		t.Errorf("second revoker calls = %v, want exactly one call for %v", b.calls, id)
	}
}

// TestRevokers_RevokeAll_JoinsErrorsFromEveryFailingRevoker asserts a
// failure in one revoker does not stop the others from running, and every
// failure is represented in the joined result.
func TestRevokers_RevokeAll_JoinsErrorsFromEveryFailingRevoker(t *testing.T) {
	t.Parallel()
	errA := errors.New("revoker a failed")
	errB := errors.New("revoker b failed")
	a := &fakeRevoker{err: errA}
	b := &fakeRevoker{err: errB}
	revokers := app.Revokers{a, b}

	err := revokers.RevokeAll(context.Background(), domain.NewUserID())
	if !errors.Is(err, errA) {
		t.Errorf("joined error does not wrap the first revoker's failure: %v", err)
	}
	if !errors.Is(err, errB) {
		t.Errorf("joined error does not wrap the second revoker's failure: %v", err)
	}
	if len(a.calls) != 1 || len(b.calls) != 1 {
		t.Error("a failing revoker must not prevent the other from being called")
	}
}

func TestRevokers_RevokeAll_EmptySliceIsNotAnError(t *testing.T) {
	t.Parallel()
	var revokers app.Revokers
	if err := revokers.RevokeAll(context.Background(), domain.NewUserID()); err != nil {
		t.Errorf("RevokeAll on an empty Revokers = %v, want nil", err)
	}
}
