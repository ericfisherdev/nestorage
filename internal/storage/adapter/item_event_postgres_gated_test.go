package adapter_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	"github.com/ericfisherdev/nestorage/internal/storage/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// itemEventFixture wires an ItemEventRepository alongside the identity
// UserRepository item_event_actor_user_id_fkey requires, over ONE derived
// database — the same "storage" suffix every other storage-package gated
// test shares (see itemFixture's own doc for why splitting it would defeat
// the harness's isolation). item_id and bin_id carry no foreign key (see
// 00012_item_event.sql's own comment), so unlike itemFixture this needs no
// Location/Bin/Item repository — a bare domain.NewItemID()/NewBinID() is a
// legitimate event target.
type itemEventFixture struct {
	pool      *pgxpool.Pool
	repo      *adapter.ItemEventRepository
	users     *identityadapter.UserRepository
	household identity.HouseholdID
}

func newItemEventFixture(t *testing.T) *itemEventFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "storage")
	return &itemEventFixture{
		pool:      pool,
		repo:      adapter.NewItemEventRepository(pool),
		users:     identityadapter.NewUserRepository(pool),
		household: seedHousehold(t, pool),
	}
}

// seedUser creates and returns a user with the given role, for
// item_event_actor_user_id_fkey and for building user principals.
func (f *itemEventFixture) seedUser(t *testing.T, role identity.Role) *identity.User {
	t.Helper()
	u := &identity.User{
		ID:           identity.NewUserID(),
		HouseholdID:  f.household,
		DisplayName:  "Maya",
		Email:        "item-event-user-" + identity.NewUserID().String() + "@example.com",
		PasswordHash: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		Role:         role,
		Color:        identity.ColorIndigo,
	}
	if err := f.users.Create(testCtx(t), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

func TestNewItemEventRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewItemEventRepository(nil) did not panic")
		}
	}()
	adapter.NewItemEventRepository(nil)
}

func TestItemEventRepository_Append_UserActorRoundTrips(t *testing.T) {
	f := newItemEventFixture(t)
	u := f.seedUser(t, identity.RoleAdult)
	actor := identity.NewUserPrincipal(u.ID, u.Role, u.DisplayName)
	actor.HouseholdID = f.household
	binID := domain.NewBinID()

	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventAdded, actor)
	e.BinID, e.BinLabel = &binID, "Bin A"
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if err := f.repo.Append(testCtx(t), &e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if e.OccurredAt.IsZero() || e.CreatedAt.IsZero() {
		t.Error("Append left a timestamp zero")
	}

	got, err := f.repo.ListByItem(testCtx(t), f.household, e.ItemID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByItem returned %d events, want 1", len(got))
	}
	if got[0].ActorKind != identity.KindUser || got[0].ActorUserID != u.ID || got[0].ActorLabel != u.DisplayName {
		t.Errorf("ListByItem[0] actor = %+v, want kind=user id=%v label=%q", got[0], u.ID, u.DisplayName)
	}
	if got[0].BinID == nil || *got[0].BinID != binID || got[0].BinLabel != "Bin A" {
		t.Errorf("ListByItem[0] bin = %+v, want %v/Bin A", got[0].BinID, binID)
	}
}

func TestItemEventRepository_Append_IntegrationActorRoundTrips(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household

	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventCreated, actor)
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if err := f.repo.Append(testCtx(t), &e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := f.repo.ListByItem(testCtx(t), f.household, e.ItemID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 1 || got[0].ActorKind != identity.KindIntegration {
		t.Fatalf("ListByItem = %+v, want one integration-attributed event", got)
	}
	if got[0].ActorUserID != (identity.UserID{}) {
		t.Errorf("ActorUserID = %v, want the zero UserID for an integration actor", got[0].ActorUserID)
	}
	if got[0].ActorLabel != "Nestova" {
		t.Errorf("ActorLabel = %q, want %q", got[0].ActorLabel, "Nestova")
	}
}

func TestItemEventRepository_Append_MovedEventRoundTripsLocations(t *testing.T) {
	f := newItemEventFixture(t)
	u := f.seedUser(t, identity.RoleAdult)
	actor := identity.NewUserPrincipal(u.ID, u.Role, u.DisplayName)
	actor.HouseholdID = f.household
	binID, fromLoc, toLoc := domain.NewBinID(), domain.NewLocationID(), domain.NewLocationID()

	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventMoved, actor)
	e.BinID, e.BinLabel = &binID, "Bin A"
	e.FromLocationID, e.FromLocationLabel = &fromLoc, "Garage"
	e.ToLocationID, e.ToLocationLabel = &toLoc, "Attic"
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if err := f.repo.Append(testCtx(t), &e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := f.repo.ListByItem(testCtx(t), f.household, e.ItemID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByItem returned %d events, want 1", len(got))
	}
	g := got[0]
	if g.FromLocationID == nil || *g.FromLocationID != fromLoc || g.FromLocationLabel != "Garage" {
		t.Errorf("from location = %v/%q, want %v/Garage", g.FromLocationID, g.FromLocationLabel, fromLoc)
	}
	if g.ToLocationID == nil || *g.ToLocationID != toLoc || g.ToLocationLabel != "Attic" {
		t.Errorf("to location = %v/%q, want %v/Attic", g.ToLocationID, g.ToLocationLabel, toLoc)
	}
}

func TestItemEventRepository_Append_EditedEventRoundTripsChangedFields(t *testing.T) {
	f := newItemEventFixture(t)
	u := f.seedUser(t, identity.RoleAdult)
	actor := identity.NewUserPrincipal(u.ID, u.Role, u.DisplayName)
	actor.HouseholdID = f.household

	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventEdited, actor)
	e.ChangedFields = []domain.EditedField{domain.FieldName, domain.FieldQuantity}
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if err := f.repo.Append(testCtx(t), &e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := f.repo.ListByItem(testCtx(t), f.household, e.ItemID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 1 || len(got[0].ChangedFields) != 2 {
		t.Fatalf("ListByItem = %+v, want one event with two changed fields", got)
	}
	if got[0].ChangedFields[0] != domain.FieldName || got[0].ChangedFields[1] != domain.FieldQuantity {
		t.Errorf("ChangedFields = %v, want [name quantity]", got[0].ChangedFields)
	}
	if got[0].BinID != nil {
		t.Errorf("BinID = %v, want nil for an edited event", got[0].BinID)
	}
}

// TestItemEventRepository_Append_EditedEventEmptyChangedFieldsRejected
// proves item_event_edit_check's array_length(...) > 0 clause at the
// database, bypassing domain.ItemEvent.Validate (which would already reject
// this) by calling Append directly.
func TestItemEventRepository_Append_EditedEventEmptyChangedFieldsRejected(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventEdited, actor)

	err := f.repo.Append(testCtx(t), &e)
	if err == nil {
		t.Error("Append(edited, no changed fields) = nil, want a check-violation error")
	}
}

// TestItemEventRepository_Append_ChangedFieldOutsideAllowedSetRejected
// proves changed_fields <@ ARRAY['name','description','quantity'] at the
// database.
func TestItemEventRepository_Append_ChangedFieldOutsideAllowedSetRejected(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventEdited, actor)
	e.ChangedFields = []domain.EditedField{domain.EditedField("color")}

	err := f.repo.Append(testCtx(t), &e)
	if err == nil {
		t.Error("Append(edited, out-of-set changed field) = nil, want a check-violation error")
	}
}

// TestItemEventRepository_Append_NonEditedEventCarryingChangedFieldsRejected
// proves item_event_edit_check's other branch: changed_fields must be NULL
// for every non-'edited' kind.
func TestItemEventRepository_Append_NonEditedEventCarryingChangedFieldsRejected(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventCreated, actor)
	e.ChangedFields = []domain.EditedField{domain.FieldName}

	err := f.repo.Append(testCtx(t), &e)
	if err == nil {
		t.Error("Append(created, carrying changed fields) = nil, want a check-violation error")
	}
}

// TestItemEventRepository_Append_UnknownKindRejected proves
// item_event_kind_check rejects a kind outside the nine known values,
// bypassing domain.ItemEvent.Validate by calling Append directly.
func TestItemEventRepository_Append_UnknownKindRejected(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventKind("archived"), actor)

	err := f.repo.Append(testCtx(t), &e)
	if err == nil {
		t.Error("Append(unknown kind) = nil, want a check-violation error")
	}
}

// TestItemEventRepository_Append_UserActorWithoutUserIDRejected proves
// item_event_actor_check's user branch at the database.
func TestItemEventRepository_Append_UserActorWithoutUserIDRejected(t *testing.T) {
	f := newItemEventFixture(t)
	e := domain.ItemEvent{
		ID: domain.NewItemEventID(), ItemID: domain.NewItemID(), ItemName: "Stove",
		Kind: domain.EventCreated, ActorKind: identity.KindUser, ActorLabel: "Ghost",
	}

	err := f.repo.Append(testCtx(t), &e)
	if err == nil {
		t.Error("Append(user actor, no actor user id) = nil, want a check-violation error")
	}
}

func TestItemEventRepository_Append_UnknownActorUserRejected(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Ghost")
	actor.HouseholdID = f.household
	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventCreated, actor)

	err := f.repo.Append(testCtx(t), &e)
	if !errors.Is(err, identity.ErrUserNotFound) {
		t.Errorf("Append(unknown actor user) = %v, want identity.ErrUserNotFound", err)
	}
}

func TestItemEventRepository_Append_NilEventRejected(t *testing.T) {
	f := newItemEventFixture(t)
	if err := f.repo.Append(testCtx(t), nil); err == nil {
		t.Error("Append(nil) = nil, want an error")
	}
}

// TestItemEventRepository_Append_DeactivatedUserStillResolves proves the
// acceptance criterion "deactivated users' past events still resolve to a
// display name": SetActive(false) soft-deletes (see identity's own doc), so
// the actor_user_id foreign key still resolves, and ActorLabel is a
// snapshot regardless of the join.
func TestItemEventRepository_Append_DeactivatedUserStillResolves(t *testing.T) {
	f := newItemEventFixture(t)
	u := f.seedUser(t, identity.RoleAdult)
	actor := identity.NewUserPrincipal(u.ID, u.Role, u.DisplayName)
	actor.HouseholdID = f.household
	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventCreated, actor)
	if err := f.repo.Append(testCtx(t), &e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if err := f.users.SetActive(testCtx(t), u.ID, false); err != nil {
		t.Fatalf("SetActive(false): %v", err)
	}

	got, err := f.repo.ListByItem(testCtx(t), f.household, e.ItemID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem after deactivation: %v", err)
	}
	if len(got) != 1 || got[0].ActorLabel != u.DisplayName || got[0].ActorUserID != u.ID {
		t.Errorf("ListByItem after deactivation = %+v, want the event unchanged", got)
	}
}

// TestItemEventRepository_ListByItem_NewestFirst proves the newest-first
// ordering the AC requires: three events, ordered by real elapsed time (a
// short sleep between each, since timestamptz has microsecond resolution —
// mirroring TestItemRepository_Move_BinToHolderToBin's own rationale).
func TestItemEventRepository_ListByItem_NewestFirst(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	itemID := domain.NewItemID()

	for _, kind := range []domain.EventKind{domain.EventCreated, domain.EventAdded, domain.EventRemoved} {
		e := domain.NewItemEvent(domain.NewItemEventID(), itemID, "Stove", kind, actor)
		if kind != domain.EventCreated {
			binID := domain.NewBinID()
			e.BinID, e.BinLabel = &binID, "Bin A"
		}
		if err := f.repo.Append(testCtx(t), &e); err != nil {
			t.Fatalf("Append(%s): %v", kind, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	got, err := f.repo.ListByItem(testCtx(t), f.household, itemID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListByItem returned %d events, want 3", len(got))
	}
	for i, want := range []domain.EventKind{domain.EventRemoved, domain.EventAdded, domain.EventCreated} {
		if got[i].Kind != want {
			t.Errorf("ListByItem[%d].Kind = %v, want %v", i, got[i].Kind, want)
		}
	}
}

// TestItemEventRepository_ListByItem_KeysetPaging proves ListByItem's
// cursor: the first page's oldest row becomes the second page's Before, and
// the pages together cover every event exactly once with no overlap.
func TestItemEventRepository_ListByItem_KeysetPaging(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	itemID := domain.NewItemID()

	for i := 0; i < 3; i++ {
		e := domain.NewItemEvent(domain.NewItemEventID(), itemID, "Stove", domain.EventCreated, actor)
		if err := f.repo.Append(testCtx(t), &e); err != nil {
			t.Fatalf("Append: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	firstPage, err := f.repo.ListByItem(testCtx(t), f.household, itemID, domain.HistoryPage{Limit: 2})
	if err != nil {
		t.Fatalf("ListByItem(page 1): %v", err)
	}
	if len(firstPage) != 2 {
		t.Fatalf("ListByItem(page 1) returned %d events, want 2", len(firstPage))
	}

	cursor := domain.HistoryCursor{OccurredAt: firstPage[1].OccurredAt, ID: firstPage[1].ID}
	secondPage, err := f.repo.ListByItem(testCtx(t), f.household, itemID, domain.HistoryPage{Limit: 2, Before: &cursor})
	if err != nil {
		t.Fatalf("ListByItem(page 2): %v", err)
	}
	if len(secondPage) != 1 {
		t.Fatalf("ListByItem(page 2) returned %d events, want 1", len(secondPage))
	}
	if secondPage[0].ID == firstPage[0].ID || secondPage[0].ID == firstPage[1].ID {
		t.Error("ListByItem(page 2) repeated a row already returned on page 1")
	}
}

// TestItemEventRepository_ListByItem_KeysetPaging_StableAcrossIdenticalOccurredAt
// proves the id tie-break: two events appended within the SAME transaction
// share the exact same occurred_at (Postgres now() is transaction-start
// time, not statement time), so the id DESC tie-break is the only thing
// keeping the page deterministic.
func TestItemEventRepository_ListByItem_KeysetPaging_StableAcrossIdenticalOccurredAt(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	itemID := domain.NewItemID()

	tx, err := f.pool.Begin(testCtx(t))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(testCtx(t)) }()
	txRepo := adapter.NewItemEventRepository(tx)

	first := domain.NewItemEvent(domain.NewItemEventID(), itemID, "Stove", domain.EventCreated, actor)
	if err := txRepo.Append(testCtx(t), &first); err != nil {
		t.Fatalf("Append(first): %v", err)
	}
	second := domain.NewItemEvent(domain.NewItemEventID(), itemID, "Stove", domain.EventAdded, actor)
	binID := domain.NewBinID()
	second.BinID, second.BinLabel = &binID, "Bin A"
	if err := txRepo.Append(testCtx(t), &second); err != nil {
		t.Fatalf("Append(second): %v", err)
	}
	if !first.OccurredAt.Equal(second.OccurredAt) {
		t.Fatalf("first.OccurredAt = %v, second.OccurredAt = %v, want equal (same transaction)", first.OccurredAt, second.OccurredAt)
	}
	if err := tx.Commit(testCtx(t)); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, err := f.repo.ListByItem(testCtx(t), f.household, itemID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByItem returned %d events, want 2", len(got))
	}
	wantFirst, wantSecond := first, second
	if second.ID.String() > first.ID.String() {
		wantFirst, wantSecond = second, first
	}
	if got[0].ID != wantFirst.ID || got[1].ID != wantSecond.ID {
		t.Errorf("ListByItem tie-break order = [%v %v], want id-descending [%v %v]", got[0].ID, got[1].ID, wantFirst.ID, wantSecond.ID)
	}
}

func TestItemEventRepository_ListByItem_Empty(t *testing.T) {
	f := newItemEventFixture(t)
	got, err := f.repo.ListByItem(testCtx(t), f.household, domain.NewItemID(), domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByItem: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByItem(no events) = %+v, want empty slice", got)
	}
}

// TestItemEventRepository_ListByBin_IncludesAddedAndRemoved proves R5/R6:
// ListByBin surfaces both 'added' (destination bin) and 'removed' (source
// bin) events for the same bin, newest first.
func TestItemEventRepository_ListByBin_IncludesAddedAndRemoved(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	binID := domain.NewBinID()

	added := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventAdded, actor)
	added.BinID, added.BinLabel = &binID, "Bin A"
	if err := f.repo.Append(testCtx(t), &added); err != nil {
		t.Fatalf("Append(added): %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	removed := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Lantern", domain.EventRemoved, actor)
	removed.BinID, removed.BinLabel = &binID, "Bin A"
	if err := f.repo.Append(testCtx(t), &removed); err != nil {
		t.Fatalf("Append(removed): %v", err)
	}

	got, err := f.repo.ListByBin(testCtx(t), f.household, binID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByBin: %v", err)
	}
	if len(got) != 2 || got[0].Kind != domain.EventRemoved || got[1].Kind != domain.EventAdded {
		t.Errorf("ListByBin = %+v, want [removed, added] newest first", got)
	}
}

// TestItemEventRepository_ListByBin_ExcludesNoBinEvents proves the partial
// index's premise: an event with no bin (here, 'edited') never appears in
// any bin's ListByBin, regardless of which bin id is queried.
func TestItemEventRepository_ListByBin_ExcludesNoBinEvents(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	binID := domain.NewBinID()

	edited := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventEdited, actor)
	edited.ChangedFields = []domain.EditedField{domain.FieldName}
	if err := f.repo.Append(testCtx(t), &edited); err != nil {
		t.Fatalf("Append(edited): %v", err)
	}

	got, err := f.repo.ListByBin(testCtx(t), f.household, binID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByBin: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByBin = %+v, want empty (edited events have no bin)", got)
	}
}

func TestItemEventRepository_ListByBin_Empty(t *testing.T) {
	f := newItemEventFixture(t)
	got, err := f.repo.ListByBin(testCtx(t), f.household, domain.NewBinID(), domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByBin: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListByBin(no events) = %+v, want empty slice", got)
	}
}

// TestItemEventRepository_ListByBin_KeysetPaging proves ListByBin's own
// cursor (NSTR-55's own history API endpoint is the first caller to ever
// pass a non-nil Before) mirrors ListByItem's exactly: the first page's
// oldest row becomes the second page's Before, and the pages together cover
// every event exactly once with no overlap.
func TestItemEventRepository_ListByBin_KeysetPaging(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	binID := domain.NewBinID()

	for i := 0; i < 3; i++ {
		e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventAdded, actor)
		e.BinID, e.BinLabel = &binID, "Bin A"
		if err := f.repo.Append(testCtx(t), &e); err != nil {
			t.Fatalf("Append: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	firstPage, err := f.repo.ListByBin(testCtx(t), f.household, binID, domain.HistoryPage{Limit: 2})
	if err != nil {
		t.Fatalf("ListByBin(page 1): %v", err)
	}
	if len(firstPage) != 2 {
		t.Fatalf("ListByBin(page 1) returned %d events, want 2", len(firstPage))
	}

	cursor := domain.HistoryCursor{OccurredAt: firstPage[1].OccurredAt, ID: firstPage[1].ID}
	secondPage, err := f.repo.ListByBin(testCtx(t), f.household, binID, domain.HistoryPage{Limit: 2, Before: &cursor})
	if err != nil {
		t.Fatalf("ListByBin(page 2): %v", err)
	}
	if len(secondPage) != 1 {
		t.Fatalf("ListByBin(page 2) returned %d events, want 1", len(secondPage))
	}
	if secondPage[0].ID == firstPage[0].ID || secondPage[0].ID == firstPage[1].ID {
		t.Error("ListByBin(page 2) repeated a row already returned on page 1")
	}
}

// TestItemEventRepository_ListByBin_KeysetPaging_StableAcrossIdenticalOccurredAt
// proves the id tie-break for ListByBin, mirroring
// TestItemEventRepository_ListByItem_KeysetPaging_StableAcrossIdenticalOccurredAt:
// two events appended within the SAME transaction share the exact same
// occurred_at (Postgres now() is transaction-start time, not statement
// time), so the id DESC tie-break is the only thing keeping the page
// deterministic.
func TestItemEventRepository_ListByBin_KeysetPaging_StableAcrossIdenticalOccurredAt(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	binID := domain.NewBinID()

	tx, err := f.pool.Begin(testCtx(t))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(testCtx(t)) }()
	txRepo := adapter.NewItemEventRepository(tx)

	first := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventAdded, actor)
	first.BinID, first.BinLabel = &binID, "Bin A"
	if err := txRepo.Append(testCtx(t), &first); err != nil {
		t.Fatalf("Append(first): %v", err)
	}
	second := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Lantern", domain.EventAdded, actor)
	second.BinID, second.BinLabel = &binID, "Bin A"
	if err := txRepo.Append(testCtx(t), &second); err != nil {
		t.Fatalf("Append(second): %v", err)
	}
	if !first.OccurredAt.Equal(second.OccurredAt) {
		t.Fatalf("first.OccurredAt = %v, second.OccurredAt = %v, want equal (same transaction)", first.OccurredAt, second.OccurredAt)
	}
	if err := tx.Commit(testCtx(t)); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, err := f.repo.ListByBin(testCtx(t), f.household, binID, domain.HistoryPage{Limit: 10})
	if err != nil {
		t.Fatalf("ListByBin: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByBin returned %d events, want 2", len(got))
	}
	wantFirst, wantSecond := first, second
	if second.ID.String() > first.ID.String() {
		wantFirst, wantSecond = second, first
	}
	if got[0].ID != wantFirst.ID || got[1].ID != wantSecond.ID {
		t.Errorf("ListByBin tie-break order = [%v %v], want id-descending [%v %v]", got[0].ID, got[1].ID, wantFirst.ID, wantSecond.ID)
	}
}

// The following three tests prove item_event's append-only enforcement:
// REVOKE UPDATE/DELETE/TRUNCATE plus the item_event_append_only trigger
// backstop both reject every raw mutation, exactly as
// domain.ItemEventRepository's own doc requires ("the interface has no
// update or delete method").

func TestItemEventTable_RawUpdateRejected(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventCreated, actor)
	if err := f.repo.Append(testCtx(t), &e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	_, err := f.pool.Exec(testCtx(t), `UPDATE item_event SET note = 'tampered' WHERE id = $1`, e.ID.String())
	if err == nil {
		t.Error("raw UPDATE on item_event = nil error, want it rejected")
	}
}

func TestItemEventTable_RawDeleteRejected(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventCreated, actor)
	if err := f.repo.Append(testCtx(t), &e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	_, err := f.pool.Exec(testCtx(t), `DELETE FROM item_event WHERE id = $1`, e.ID.String())
	if err == nil {
		t.Error("raw DELETE on item_event = nil error, want it rejected")
	}
}

func TestItemEventTable_RawTruncateRejected(t *testing.T) {
	f := newItemEventFixture(t)
	actor := identity.NewIntegrationPrincipal("Nestova")
	actor.HouseholdID = f.household
	e := domain.NewItemEvent(domain.NewItemEventID(), domain.NewItemID(), "Stove", domain.EventCreated, actor)
	if err := f.repo.Append(testCtx(t), &e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	_, err := f.pool.Exec(testCtx(t), `TRUNCATE item_event`)
	if err == nil {
		t.Error("raw TRUNCATE on item_event = nil error, want it rejected")
	}
}
