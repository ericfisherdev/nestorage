package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/labels/domain"
)

func TestBatchTooLargeError_UnwrapsToSentinel(t *testing.T) {
	err := &domain.BatchTooLargeError{Count: 301, Max: domain.MaxBatchLabels}
	if !errors.Is(err, domain.ErrBatchTooLarge) {
		t.Errorf("errors.Is(err, ErrBatchTooLarge) = false, want true for %#v", err)
	}
}

func TestBatchTooLargeError_ErrorNamesCountAndMax(t *testing.T) {
	err := &domain.BatchTooLargeError{Count: 301, Max: 300}
	got := err.Error()
	if !strings.Contains(got, "301") || !strings.Contains(got, "300") {
		t.Errorf("Error() = %q, want it to name both the count (301) and the max (300)", got)
	}
}
