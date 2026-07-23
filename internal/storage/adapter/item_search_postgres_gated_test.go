package adapter_test

import (
	"errors"
	"strings"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// TestItemRepository_FindVisibleDetail_InBin proves the joined bin/location
// columns FindVisibleDetail adds over Get: an in-bin item's BinCode/
// LocationName are populated and HolderName is empty, mirroring
// TestItemRepository_CreateAndGet's own in-bin fixture.
func TestItemRepository_FindVisibleDetail_InBin(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	it := newItem("Camping stove", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.FindVisibleDetail(testCtx(t), viewer, it.ID)
	if err != nil {
		t.Fatalf("FindVisibleDetail: %v", err)
	}
	if got.Item.ID != it.ID || got.Item.Name != "Camping stove" {
		t.Errorf("FindVisibleDetail.Item = %+v, want it to match the created item", got.Item)
	}
	if got.BinCode == "" {
		t.Error("FindVisibleDetail(in-bin item) missing BinCode")
	}
	if got.LocationName != "Garage" {
		t.Errorf("FindVisibleDetail.LocationName = %q, want %q", got.LocationName, "Garage")
	}
	if got.HolderName != "" {
		t.Errorf("FindVisibleDetail(in-bin item).HolderName = %q, want empty", got.HolderName)
	}
}

// TestItemRepository_FindVisibleDetail_CheckedOut proves the held-item half:
// HolderName/HolderColor are populated from the joined app_user row and
// BinCode/LocationName stay empty, mirroring
// TestItemRepository_Create_HeldByRoundTrips' own held fixture.
func TestItemRepository_FindVisibleDetail_CheckedOut(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	holder := f.seedUser(t, identity.RoleMember)
	it := &domain.Item{ID: domain.NewItemID(), Name: "Sleeping bag", Quantity: 1, HeldBy: &holder, CreatedBy: creator}
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.FindVisibleDetail(testCtx(t), viewer, it.ID)
	if err != nil {
		t.Fatalf("FindVisibleDetail: %v", err)
	}
	if got.HolderName != "Test User" {
		t.Errorf("FindVisibleDetail(held item).HolderName = %q, want %q", got.HolderName, "Test User")
	}
	if got.HolderColor != identity.ColorIndigo {
		t.Errorf("FindVisibleDetail(held item).HolderColor = %q, want %q", got.HolderColor, identity.ColorIndigo)
	}
	if got.BinCode != "" || got.LocationName != "" {
		t.Errorf("FindVisibleDetail(held item) = %+v, want no bin/location", got)
	}
}

func TestItemRepository_FindVisibleDetail_NotFound(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	_, err := f.repo.FindVisibleDetail(testCtx(t), viewer, domain.NewItemID())
	if !errors.Is(err, domain.ErrItemNotFound) {
		t.Errorf("FindVisibleDetail(unknown) = %v, want ErrItemNotFound", err)
	}
}

// TestItemRepository_FindVisibleDetail_VisibilityMatrix mirrors
// TestItemRepository_VisibilityMatrix's own matrix (item_postgres_gated_test.go)
// over FindVisibleDetail's identical visibility predicate.
func TestItemRepository_FindVisibleDetail_VisibilityMatrix(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	other := f.seedUser(t, identity.RoleMember)
	admin := f.seedUser(t, identity.RoleAdmin)
	loc := f.seedLocation(t, creator)
	privateBin := f.seedBin(t, creator, loc, domain.VisibilityPrivate)
	it := newItem("Private item", privateBin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	principals := []struct {
		name    string
		p       identity.Principal
		visible bool
	}{
		{"admin", identity.NewUserPrincipal(admin, identity.RoleAdmin, "Admin"), true},
		{"creator", identity.NewUserPrincipal(creator, identity.RoleMember, "Creator"), true},
		{"non-creator member", identity.NewUserPrincipal(other, identity.RoleMember, "Other"), false},
		{"integration", identity.NewIntegrationPrincipal("Nestova"), false},
		{"anonymous", identity.Principal{}, false},
	}
	for _, pr := range principals {
		t.Run(pr.name, func(t *testing.T) {
			_, err := f.repo.FindVisibleDetail(testCtx(t), pr.p, it.ID)
			if pr.visible && err != nil {
				t.Errorf("FindVisibleDetail(%s) = %v, want nil (visible)", pr.name, err)
			}
			if !pr.visible && !errors.Is(err, domain.ErrItemNotFound) {
				t.Errorf("FindVisibleDetail(%s) = %v, want ErrItemNotFound (not visible)", pr.name, err)
			}
		})
	}
}

// TestItemRepository_SearchVisible_MatchesByItemName proves the basic
// trigram substring match over the item's own name, and that a
// non-matching sibling in the same bin is excluded.
func TestItemRepository_SearchVisible_MatchesByItemName(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	target := newItem("Camping Stove Deluxe", bin, creator)
	if err := f.repo.Create(testCtx(t), target); err != nil {
		t.Fatalf("Create(target): %v", err)
	}
	if err := f.repo.Create(testCtx(t), newItem("Lantern", bin, creator)); err != nil {
		t.Fatalf("Create(decoy): %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.SearchVisible(testCtx(t), viewer, "stove", 10)
	if err != nil {
		t.Fatalf("SearchVisible: %v", err)
	}
	if len(got) != 1 || got[0].ID != target.ID {
		t.Errorf("SearchVisible(\"stove\") = %+v, want exactly the stove item", got)
	}
	if got[0].State != domain.StateInBin || got[0].BinCode == "" {
		t.Errorf("SearchVisible(\"stove\")[0] = %+v, want an in-bin result with a bin code", got[0])
	}
}

// TestItemRepository_SearchVisible_MatchesByBinName proves a bin-name
// substring surfaces the items sitting inside it, per the ticket's own
// "searching a bin or location name surfaces the items inside it" rule.
func TestItemRepository_SearchVisible_MatchesByBinName(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	binID := domain.NewBinID()
	bin := &domain.Bin{
		ID: binID, Code: "UNIQ" + binID.String(), Name: "Holiday Decorations Bin",
		LocationID: loc, CreatedBy: creator, Visibility: domain.VisibilityPublic,
	}
	if err := f.bins.Create(testCtx(t), bin); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	it := newItem("Ornaments", binID, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.SearchVisible(testCtx(t), viewer, "Holiday Decorations", 10)
	if err != nil {
		t.Fatalf("SearchVisible: %v", err)
	}
	if len(got) != 1 || got[0].ID != it.ID {
		t.Errorf("SearchVisible(bin name) = %+v, want the item inside that bin", got)
	}
}

// TestItemRepository_SearchVisible_MatchesByLocationName is the location
// half of MatchesByBinName's own doc.
func TestItemRepository_SearchVisible_MatchesByLocationName(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	locID := domain.NewLocationID()
	loc := &domain.Location{ID: locID, Name: "Attic Storage Nook", CreatedBy: creator}
	if err := f.locations.Create(testCtx(t), loc); err != nil {
		t.Fatalf("seed location: %v", err)
	}
	bin := f.seedBin(t, creator, locID, domain.VisibilityPublic)
	it := newItem("Box of photos", bin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.SearchVisible(testCtx(t), viewer, "Attic Storage", 10)
	if err != nil {
		t.Fatalf("SearchVisible: %v", err)
	}
	if len(got) != 1 || got[0].ID != it.ID {
		t.Errorf("SearchVisible(location name) = %+v, want the item in that location", got)
	}
}

// TestItemRepository_SearchVisible_HeldItemMatchesOwnNameOnly proves the
// held-item exception: a checked-out item is found by its own name, reports
// StateCheckedOut and a HolderName, and carries no bin/location (both
// columns are NULL for that row's LEFT JOINs).
func TestItemRepository_SearchVisible_HeldItemMatchesOwnNameOnly(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	it := &domain.Item{ID: domain.NewItemID(), Name: "Unique Held Flashlight", Quantity: 1, HeldBy: &creator, CreatedBy: creator}
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.SearchVisible(testCtx(t), viewer, "Flashlight", 10)
	if err != nil {
		t.Fatalf("SearchVisible: %v", err)
	}
	if len(got) != 1 || got[0].ID != it.ID {
		t.Fatalf("SearchVisible(held item name) = %+v, want the held item", got)
	}
	if got[0].State != domain.StateCheckedOut {
		t.Errorf("SearchVisible(held item)[0].State = %q, want %q", got[0].State, domain.StateCheckedOut)
	}
	if got[0].HolderName == "" {
		t.Error("SearchVisible(held item) missing HolderName")
	}
	if got[0].BinCode != "" || got[0].LocationName != "" {
		t.Errorf("SearchVisible(held item)[0] = %+v, want no bin/location", got[0])
	}
}

// TestItemRepository_SearchVisible_ExcludesPrivateBinForNonOwner is
// SearchVisible's own version of TestItemRepository_ListByBin_ScopedToVisibility:
// a non-owner's search must never surface an item sitting in another
// member's private bin, while the owner's own search still finds it.
func TestItemRepository_SearchVisible_ExcludesPrivateBinForNonOwner(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	other := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	privateBin := f.seedBin(t, creator, loc, domain.VisibilityPrivate)
	it := newItem("Unique Private Widget", privateBin, creator)
	if err := f.repo.Create(testCtx(t), it); err != nil {
		t.Fatalf("Create: %v", err)
	}

	otherViewer := identity.NewUserPrincipal(other, identity.RoleMember, "Other")
	got, err := f.repo.SearchVisible(testCtx(t), otherViewer, "Private Widget", 10)
	if err != nil {
		t.Fatalf("SearchVisible(non-owner): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("SearchVisible(non-owner, private bin item) = %+v, want no results", got)
	}

	creatorViewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err = f.repo.SearchVisible(testCtx(t), creatorViewer, "Private Widget", 10)
	if err != nil {
		t.Fatalf("SearchVisible(creator): %v", err)
	}
	if len(got) != 1 || got[0].ID != it.ID {
		t.Errorf("SearchVisible(creator, own private bin item) = %+v, want the item", got)
	}
}

// TestItemRepository_SearchVisible_EscapesWildcardCharacters proves a
// literal % or _ in the search term is matched literally, not treated as a
// wildcard: an unescaped term would also match the decoy (its "%"/"_"
// positions absorb the decoy's own different characters), so this only
// passes if escapeLikeTerm actually ran.
func TestItemRepository_SearchVisible_EscapesWildcardCharacters(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)
	target := newItem("100%_Cotton_Towel", bin, creator)
	if err := f.repo.Create(testCtx(t), target); err != nil {
		t.Fatalf("Create(target): %v", err)
	}
	decoy := newItem("100X0Cotton0Towel", bin, creator)
	if err := f.repo.Create(testCtx(t), decoy); err != nil {
		t.Fatalf("Create(decoy): %v", err)
	}

	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")
	got, err := f.repo.SearchVisible(testCtx(t), viewer, "100%_Cotton", 10)
	if err != nil {
		t.Fatalf("SearchVisible: %v", err)
	}
	if len(got) != 1 || got[0].ID != target.ID {
		t.Errorf(`SearchVisible("100%%_Cotton") = %+v, want only the literal match, not the wildcard decoy`, got)
	}
}

func TestItemRepository_SearchVisible_NoMatchIsEmptySlice(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	viewer := identity.NewUserPrincipal(creator, identity.RoleMember, "Creator")

	got, err := f.repo.SearchVisible(testCtx(t), viewer, "nonexistent-zzz", 10)
	if err != nil {
		t.Fatalf("SearchVisible: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("SearchVisible(no match) = %+v, want an empty slice", got)
	}
}

// TestItemRepository_SearchVisible_UsesTrigramIndex satisfies the AC's
// "verify the index is used" and 10,000-item responsiveness criteria: with
// enough item rows and fresh planner statistics, an ILIKE search over
// item.name is planned as a Bitmap Index Scan on item_name_trgm, never a
// sequential scan over the item table.
func TestItemRepository_SearchVisible_UsesTrigramIndex(t *testing.T) {
	f := newItemFixture(t)
	creator := f.seedUser(t, identity.RoleMember)
	loc := f.seedLocation(t, creator)
	bin := f.seedBin(t, creator, loc, domain.VisibilityPublic)

	// Bulk-seed filler rows directly via SQL, bypassing the repository (and
	// using gen_random_uuid() rather than domain.NewItemID(), already used
	// elsewhere in this codebase as a column DEFAULT — see 00008_item.sql's
	// own comment contrasting it with the app-supplied UUIDv7): these rows
	// exist to give the query planner real data to plan against. Empirically
	// verified against this exact table shape (narrow rows, one bin, a
	// single-word highly-selective target), repeated across several ANALYZE
	// runs to rule out ANALYZE's own random-sample estimates flip-flopping
	// the choice near a crossover: Postgres' planner still prefers a
	// sequential scan at the AC's own "at least 10,000 items" floor (reading
	// the whole narrow table costs less than bitmap-probing it) and even at
	// 30,000, only settling on the trigram index — reliably, run after run —
	// at 100,000, the row count used here. Inserting one row at a time
	// through the repository would make this test impractically slow.
	const fillerCount = 100000
	_, err := f.pool.Exec(testCtx(t), `
		INSERT INTO item (id, name, quantity, current_bin_id, created_by)
		SELECT gen_random_uuid(), 'Filler item ' || gs, 1, $1, $2
		FROM generate_series(1, $3) AS gs`,
		bin.String(), creator.String(), fillerCount,
	)
	if err != nil {
		t.Fatalf("bulk-seed filler items: %v", err)
	}

	target := newItem("Extremely Unique Target Widget", bin, creator)
	if err := f.repo.Create(testCtx(t), target); err != nil {
		t.Fatalf("Create(target): %v", err)
	}

	if _, err := f.pool.Exec(testCtx(t), "ANALYZE item"); err != nil {
		t.Fatalf("ANALYZE item: %v", err)
	}

	rows, err := f.pool.Query(testCtx(t), `EXPLAIN SELECT id FROM item WHERE name ILIKE '%extremely unique target widget%'`)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan EXPLAIN line: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("EXPLAIN rows: %v", err)
	}

	planText := plan.String()
	if !strings.Contains(planText, "item_name_trgm") {
		t.Errorf("query plan did not use the item_name_trgm index:\n%s", planText)
	}
	if !strings.Contains(planText, "Bitmap Index Scan") {
		t.Errorf("query plan did not use a Bitmap Index Scan:\n%s", planText)
	}
	if strings.Contains(planText, "Seq Scan on item") {
		t.Errorf("query plan used a sequential scan on item instead of the trigram index:\n%s", planText)
	}
}
