package adapter

// This file is package adapter (white-box), not adapter_test — a deliberate,
// narrow exception to this directory's usual black-box-only test convention
// (see items_web_test.go/bins_web_test.go), needed only because
// daysBetween/historyDayHeading/buildItemHistoryDays are unexported and the
// AC this ticket must prove ("Dates display correctly for a non-UTC
// viewer") requires deterministic control over the zone a timestamp is
// expressed in — control the HTTP-level tests in item_history_web_test.go
// cannot exercise without depending on the test process's own system
// timezone.

import (
	"testing"
	"time"

	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func TestDaysBetween(t *testing.T) {
	// zone is an arbitrary non-UTC offset, fixed rather than a named/loaded
	// location, so this test's outcome never depends on the machine it runs
	// on (unlike time.Local, which does).
	zone := time.FixedZone("UTC-5", -5*60*60)

	tests := []struct {
		name           string
		earlier, later time.Time
		want           int
	}{
		{
			name:    "same instant",
			earlier: time.Date(2026, 7, 20, 12, 0, 0, 0, zone),
			later:   time.Date(2026, 7, 20, 12, 0, 0, 0, zone),
			want:    0,
		},
		{
			// The regression this guards: 23:30 and 00:30 are only one hour
			// apart and share the same UTC calendar day (04:30 and 05:30
			// UTC), but they fall on different LOCAL calendar days in zone
			// — a UTC-derived day boundary would wrongly report 0.
			name:    "crosses a local midnight within the same UTC calendar day",
			earlier: time.Date(2026, 7, 20, 23, 30, 0, 0, zone),
			later:   time.Date(2026, 7, 21, 0, 30, 0, 0, zone),
			want:    1,
		},
		{
			name:    "two full local days apart",
			earlier: time.Date(2026, 7, 18, 9, 0, 0, 0, zone),
			later:   time.Date(2026, 7, 20, 21, 0, 0, 0, zone),
			want:    2,
		},
		{
			name:    "later precedes earlier",
			earlier: time.Date(2026, 7, 20, 12, 0, 0, 0, zone),
			later:   time.Date(2026, 7, 19, 12, 0, 0, 0, zone),
			want:    -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := daysBetween(tt.earlier, tt.later); got != tt.want {
				t.Errorf("daysBetween(%v, %v) = %d, want %d", tt.earlier, tt.later, got, tt.want)
			}
		})
	}
}

func TestHistoryDayHeading(t *testing.T) {
	now := time.Date(2026, 7, 20, 15, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"today", now, "Today"},
		{"yesterday", now.AddDate(0, 0, -1), "Yesterday"},
		{"ten days ago falls back to the calendar date", now.AddDate(0, 0, -10), now.AddDate(0, 0, -10).Local().Format("Jan 2, 2006")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := historyDayHeading(tt.t, now); got != tt.want {
				t.Errorf("historyDayHeading(%v, %v) = %q, want %q", tt.t, now, got, tt.want)
			}
		})
	}
}

func TestBuildItemHistoryDays_GroupsAcrossALocalMidnightBoundary(t *testing.T) {
	zone := time.FixedZone("UTC-5", -5*60*60)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, zone)

	// Newest-first, as ListByItem's own contract guarantees: the second
	// event is only one hour before the first but crosses zone's own local
	// midnight, so it must land in a separate (earlier) day group.
	events := []domain.ItemEvent{
		{Kind: domain.EventReturned, BinLabel: "B-01 — Garage", OccurredAt: time.Date(2026, 7, 21, 0, 30, 0, 0, zone)},
		{Kind: domain.EventRemoved, BinLabel: "B-01 — Garage", OccurredAt: time.Date(2026, 7, 20, 23, 30, 0, 0, zone)},
	}

	days := buildItemHistoryDays(events, now)
	if len(days) != 2 {
		t.Fatalf("buildItemHistoryDays produced %d day groups, want 2 (events straddle a local midnight)", len(days))
	}
	if len(days[0].Events) != 1 || len(days[1].Events) != 1 {
		t.Errorf("day groups = %d/%d events, want 1/1", len(days[0].Events), len(days[1].Events))
	}
}

func TestBuildItemHistoryDays_SameLocalDayStaysOneGroup(t *testing.T) {
	zone := time.FixedZone("UTC-5", -5*60*60)
	now := time.Date(2026, 7, 20, 20, 0, 0, 0, zone)

	events := []domain.ItemEvent{
		{Kind: domain.EventReturned, BinLabel: "B-01 — Garage", OccurredAt: time.Date(2026, 7, 20, 18, 0, 0, 0, zone)},
		{Kind: domain.EventRemoved, BinLabel: "B-01 — Garage", OccurredAt: time.Date(2026, 7, 20, 9, 0, 0, 0, zone)},
	}

	days := buildItemHistoryDays(events, now)
	if len(days) != 1 {
		t.Fatalf("buildItemHistoryDays produced %d day groups, want 1 (both events fall on the same local day)", len(days))
	}
	if days[0].Heading != "Today" {
		t.Errorf("heading = %q, want %q", days[0].Heading, "Today")
	}
	if len(days[0].Events) != 2 {
		t.Errorf("day group has %d events, want 2", len(days[0].Events))
	}
}
