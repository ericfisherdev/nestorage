package domain_test

import (
	"errors"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// TestNewItemEventID_RoundTripsThroughParse mirrors
// TestNewItemLinkID_RoundTripsThroughParse: NewItemEventID/String/
// ParseItemEventID are new code this ticket adds to ids.go.
func TestNewItemEventID_RoundTripsThroughParse(t *testing.T) {
	id := domain.NewItemEventID()
	if id == (domain.ItemEventID{}) {
		t.Fatal("NewItemEventID returned the zero value")
	}

	parsed, err := domain.ParseItemEventID(id.String())
	if err != nil {
		t.Fatalf("ParseItemEventID(%q) error = %v, want nil", id.String(), err)
	}
	if parsed != id {
		t.Errorf("ParseItemEventID round trip = %v, want %v", parsed, id)
	}
}

func TestParseItemEventID_Invalid(t *testing.T) {
	if _, err := domain.ParseItemEventID("not-a-uuid"); err == nil {
		t.Error("ParseItemEventID(invalid) error = nil, want an error")
	}
}

func TestNewItemEventID_Unique(t *testing.T) {
	first := domain.NewItemEventID()
	second := domain.NewItemEventID()
	if first == second {
		t.Error("NewItemEventID returned the same id twice")
	}
}

// TestParseEventKind_AllNineAccepted proves every kind item_event_kind_check
// permits round-trips through ParseEventKind — the acceptance criterion
// "unknown event kinds are rejected at parse time" is only meaningful once
// every KNOWN kind is proven accepted first.
func TestParseEventKind_AllNineAccepted(t *testing.T) {
	kinds := []domain.EventKind{
		domain.EventCreated, domain.EventAdded, domain.EventRemoved, domain.EventReturned,
		domain.EventMoved, domain.EventDeleted, domain.EventReturnRequested,
		domain.EventReturnRequestCancelled, domain.EventEdited,
	}
	if len(kinds) != 9 {
		t.Fatalf("test lists %d kinds, want 9", len(kinds))
	}
	for _, k := range kinds {
		t.Run(k.String(), func(t *testing.T) {
			got, err := domain.ParseEventKind(k.String())
			if err != nil {
				t.Fatalf("ParseEventKind(%q) error = %v, want nil", k, err)
			}
			if got != k {
				t.Errorf("ParseEventKind(%q) = %q, want %q", k, got, k)
			}
			if !k.Valid() {
				t.Errorf("%q.Valid() = false, want true", k)
			}
		})
	}
}

func TestParseEventKind_UnknownRejected(t *testing.T) {
	_, err := domain.ParseEventKind("archived")
	if !errors.Is(err, domain.ErrInvalidEventKind) {
		t.Errorf("ParseEventKind(archived) = %v, want wrapped ErrInvalidEventKind", err)
	}
}

func TestParseEditedField_AllThreeAccepted(t *testing.T) {
	fields := []domain.EditedField{domain.FieldName, domain.FieldDescription, domain.FieldQuantity}
	for _, f := range fields {
		t.Run(f.String(), func(t *testing.T) {
			got, err := domain.ParseEditedField(f.String())
			if err != nil {
				t.Fatalf("ParseEditedField(%q) error = %v, want nil", f, err)
			}
			if got != f {
				t.Errorf("ParseEditedField(%q) = %q, want %q", f, got, f)
			}
		})
	}
}

func TestParseEditedField_UnknownRejected(t *testing.T) {
	_, err := domain.ParseEditedField("color")
	if !errors.Is(err, domain.ErrInvalidEditedField) {
		t.Errorf("ParseEditedField(color) = %v, want wrapped ErrInvalidEditedField", err)
	}
}

// TestNewItemEvent_UserPrincipalAttribution covers both credentials that
// resolve to a KindUser principal (session and device token share the same
// Principal shape, so NewUserPrincipal alone exercises both).
func TestNewItemEvent_UserPrincipalAttribution(t *testing.T) {
	userID := identity.NewUserID()
	actor := identity.NewUserPrincipal(userID, identity.RoleAdult, "Maya")

	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventCreated, actor)

	if e.ActorKind != identity.KindUser {
		t.Errorf("ActorKind = %v, want KindUser", e.ActorKind)
	}
	if e.ActorUserID != userID {
		t.Errorf("ActorUserID = %v, want %v", e.ActorUserID, userID)
	}
	if e.ActorLabel != "Maya" {
		t.Errorf("ActorLabel = %q, want %q", e.ActorLabel, "Maya")
	}
}

// TestNewItemEvent_IntegrationPrincipalAttribution covers the account api
// key credential: no user behind it, so ActorUserID stays the zero UserID
// while ActorLabel still carries the key's label.
func TestNewItemEvent_IntegrationPrincipalAttribution(t *testing.T) {
	actor := identity.NewIntegrationPrincipal("Nestova")

	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventCreated, actor)

	if e.ActorKind != identity.KindIntegration {
		t.Errorf("ActorKind = %v, want KindIntegration", e.ActorKind)
	}
	if e.ActorUserID != (identity.UserID{}) {
		t.Errorf("ActorUserID = %v, want the zero UserID", e.ActorUserID)
	}
	if e.ActorLabel != "Nestova" {
		t.Errorf("ActorLabel = %q, want %q", e.ActorLabel, "Nestova")
	}
}

// baseEvent returns a Validate-passing EventCreated event, the starting
// point every TestItemEvent_Validate case mutates.
func baseEvent(t *testing.T) domain.ItemEvent {
	t.Helper()
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Maya")
	return domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventCreated, actor)
}

func TestItemEvent_Validate(t *testing.T) {
	binID := domain.NewBinID()
	locA, locB := domain.NewLocationID(), domain.NewLocationID()

	tests := []struct {
		name    string
		mutate  func(e *domain.ItemEvent)
		wantErr bool
	}{
		{"created event with no bin/location/changed-fields is valid", func(*domain.ItemEvent) {}, false},
		{
			"added event with bin set is valid",
			func(e *domain.ItemEvent) {
				e.Kind = domain.EventAdded
				e.BinID, e.BinLabel = &binID, "Bin A"
			},
			false,
		},
		{"unknown kind rejected", func(e *domain.ItemEvent) { e.Kind = domain.EventKind("archived") }, true},
		{"blank item name rejected", func(e *domain.ItemEvent) { e.ItemName = "   " }, true},
		{"blank actor label rejected", func(e *domain.ItemEvent) { e.ActorLabel = "" }, true},
		{
			"user actor missing actor user id rejected",
			func(e *domain.ItemEvent) { e.ActorUserID = identity.UserID{} },
			true,
		},
		{
			"integration actor carrying actor user id rejected",
			func(e *domain.ItemEvent) { e.ActorKind = identity.KindIntegration },
			true,
		},
		{"unknown actor kind rejected", func(e *domain.ItemEvent) { e.ActorKind = identity.Kind("robot") }, true},
		{"bin id without bin label rejected", func(e *domain.ItemEvent) { e.BinID = &binID }, true},
		{"bin label without bin id rejected", func(e *domain.ItemEvent) { e.BinLabel = "Bin A" }, true},
		{
			"moved event missing location fields rejected",
			func(e *domain.ItemEvent) {
				e.Kind = domain.EventMoved
				e.BinID, e.BinLabel = &binID, "Bin A"
			},
			true,
		},
		{
			"moved event with all four location fields is valid",
			func(e *domain.ItemEvent) {
				e.Kind = domain.EventMoved
				e.BinID, e.BinLabel = &binID, "Bin A"
				e.FromLocationID, e.FromLocationLabel = &locA, "Garage"
				e.ToLocationID, e.ToLocationLabel = &locB, "Attic"
			},
			false,
		},
		{
			"non-moved event carrying a location field rejected",
			func(e *domain.ItemEvent) { e.FromLocationID, e.FromLocationLabel = &locA, "Garage" },
			true,
		},
		{
			"edited event with empty changed fields rejected",
			func(e *domain.ItemEvent) { e.Kind = domain.EventEdited },
			true,
		},
		{
			"edited event with an invalid changed field rejected",
			func(e *domain.ItemEvent) {
				e.Kind = domain.EventEdited
				e.ChangedFields = []domain.EditedField{domain.EditedField("color")}
			},
			true,
		},
		{
			"edited event with valid changed fields is valid",
			func(e *domain.ItemEvent) {
				e.Kind = domain.EventEdited
				e.ChangedFields = []domain.EditedField{domain.FieldName, domain.FieldQuantity}
			},
			false,
		},
		{
			"non-edited event carrying changed fields rejected",
			func(e *domain.ItemEvent) { e.ChangedFields = []domain.EditedField{domain.FieldName} },
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := baseEvent(t)
			tt.mutate(&e)
			err := e.Validate()
			if tt.wantErr && !errors.Is(err, domain.ErrInvalidItemEvent) {
				t.Errorf("Validate() = %v, want wrapped ErrInvalidItemEvent", err)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

// TestHistoryPage_ZeroValueHasNoCursor documents HistoryPage's first-page
// contract: a nil Before, not a zero time.Time cursor, is what "no cursor
// yet" means.
func TestHistoryPage_ZeroValueHasNoCursor(t *testing.T) {
	var page domain.HistoryPage
	if page.Before != nil {
		t.Errorf("zero-value HistoryPage.Before = %v, want nil", page.Before)
	}
	if page.Limit != 0 {
		t.Errorf("zero-value HistoryPage.Limit = %d, want 0", page.Limit)
	}
}
