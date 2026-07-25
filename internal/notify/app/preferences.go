package app

import (
	"context"
	"fmt"
	"log/slog"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

// EventTypeSection is one row of PreferenceService.PreferencesForUser's
// result: one event type's own current settings-screen state. InApp is
// always true — rendered as an always-on informational row, never a
// switch (see domain.EffectiveChannels' own doc) — EmailEnabled reflects
// whatever is actually stored (false when nothing is).
type EventTypeSection struct {
	EventType    domain.EventType
	InApp        bool
	EmailEnabled bool
}

// PreferenceService is the notify context's own implementation of
// domain.PreferenceReader (via ChannelsFor) plus the settings screen's
// read/write surface (PreferencesForUser, SetEmailEnabled). Swapped in at
// cmd/server/main.go for NSTR-44's app.StaticPreferenceReader, with no
// change to Notifier or its callers (see StaticPreferenceReader's own doc).
type PreferenceService struct {
	repo   domain.PreferenceRepository
	logger *slog.Logger
}

// NewPreferenceService constructs PreferenceService. Both dependencies are
// required; a missing one panics at construction time, matching every
// other constructor in this codebase. Unlike NSTR-44's own StaticPreferenceReader,
// this takes no fixed channel slice to inject: with ChannelInApp
// structurally always on and ChannelEmail the only toggle, the settings
// screen can never offer an undeliverable channel by construction (Sprint 6
// reconciliation R12).
func NewPreferenceService(repo domain.PreferenceRepository, logger *slog.Logger) *PreferenceService {
	if repo == nil {
		panic("notify/app: NewPreferenceService requires a non-nil domain.PreferenceRepository")
	}
	if logger == nil {
		panic("notify/app: NewPreferenceService requires a non-nil logger")
	}
	return &PreferenceService{repo: repo, logger: logger}
}

// Compile-time assurance PreferenceService satisfies the port Notifier
// depends on.
var _ domain.PreferenceReader = (*PreferenceService)(nil)

// ChannelsFor implements domain.PreferenceReader: resolves userID's stored
// preference for eventType and merges it with the fixed default. Consulted
// by Notifier at enqueue time, with no caching, which is what makes a
// preference change take effect on the very next notification without a
// restart. Never returns an empty slice (domain.EffectiveChannels' own
// guarantee).
func (s *PreferenceService) ChannelsFor(ctx context.Context, userID identity.UserID, eventType domain.EventType) ([]domain.Channel, error) {
	stored, err := s.repo.Get(ctx, userID, eventType)
	if err != nil {
		return nil, fmt.Errorf("notify/app: channels for: %w", err)
	}
	return domain.EffectiveChannels(stored), nil
}

// PreferencesForUser returns the full, defaults-merged settings matrix for
// userID: one EventTypeSection per known domain.EventType, InApp always
// true, EmailEnabled reflecting any stored row. Backs the settings screen's
// GET.
func (s *PreferenceService) PreferencesForUser(ctx context.Context, userID identity.UserID) ([]EventTypeSection, error) {
	stored, err := s.repo.ListForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("notify/app: preferences for user: %w", err)
	}
	emailEnabled := make(map[domain.EventType]bool, len(stored))
	for _, p := range stored {
		if p.Channel == domain.ChannelEmail {
			emailEnabled[p.EventType] = p.Enabled
		}
	}

	sections := make([]EventTypeSection, 0, len(knownEventTypes))
	for _, et := range knownEventTypes {
		sections = append(sections, EventTypeSection{EventType: et, InApp: true, EmailEnabled: emailEnabled[et]})
	}
	return sections, nil
}

// SetEmailEnabled validates eventType and upserts userID's own email
// preference row for it. There is deliberately no generic SetChannel:
// ChannelInApp is structurally not settable through this service at all
// (see domain.ErrChannelNotConfigurable's own doc for why no call path
// here can even reach it).
func (s *PreferenceService) SetEmailEnabled(ctx context.Context, userID identity.UserID, eventType domain.EventType, enabled bool) error {
	if !eventType.Valid() {
		return fmt.Errorf("notify/app: set email enabled: %w: %q", domain.ErrInvalidEventType, eventType)
	}
	pref := domain.Preference{UserID: userID, EventType: eventType, Channel: domain.ChannelEmail, Enabled: enabled}
	if err := s.repo.Upsert(ctx, pref); err != nil {
		return fmt.Errorf("notify/app: set email enabled: %w", err)
	}
	return nil
}

// knownEventTypes lists every domain.EventType PreferencesForUser renders a
// section for, in display order (return_requested first: it is the one a
// household member acts on soonest — the request is still open — while
// item_returned is a closed-loop confirmation).
var knownEventTypes = []domain.EventType{domain.EventTypeReturnRequested, domain.EventTypeItemReturned}
