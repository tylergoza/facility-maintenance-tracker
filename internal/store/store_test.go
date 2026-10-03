package store

import (
	"cmp"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAddInterval(t *testing.T) {
	cases := []struct {
		date  string
		value int
		unit  string
		want  string
	}{
		{"2026-01-31", 1, "months", "2026-02-28"},
		{"2024-01-31", 1, "months", "2024-02-29"},
		{"2026-03-15", 3, "months", "2026-06-15"},
		{"2024-02-29", 1, "years", "2025-02-28"},
		{"2026-12-30", 1, "weeks", "2027-01-06"},
		{"2026-09-30", 10, "days", "2026-10-10"},
	}
	for _, c := range cases {
		got, err := AddInterval(c.date, c.value, c.unit)
		if err != nil || got != c.want {
			t.Errorf("AddInterval(%s, %d, %s) = %s, %v; want %s", c.date, c.value, c.unit, got, err, c.want)
		}
	}
}

func TestStatusFor(t *testing.T) {
	today := time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)
	cases := map[string]Status{
		"":           StatusUnscheduled,
		"2026-09-29": StatusOverdue,
		"2026-09-30": StatusDueSoon,
		"2026-10-30": StatusDueSoon,
		"2026-10-31": StatusUpcoming,
	}
	for due, want := range cases {
		if got := StatusFor(due, today, 30); got != want {
			t.Errorf("StatusFor(%q) = %s, want %s", due, got, want)
		}
	}
}

func TestCompareRoomNumbers(t *testing.T) {
	sorted := []string{"2", "009", "10", "101", "101a", "b2", "B10", "Gym", ""}
	for i := range sorted {
		for j := range sorted {
			got, want := CompareRoomNumbers(sorted[i], sorted[j]), cmp.Compare(i, j)
			if got != want {
				t.Errorf("CompareRoomNumbers(%q, %q) = %d, want %d", sorted[i], sorted[j], got, want)
			}
		}
	}
}

// addPlace saves a place inside parent (nil for a site).
func addPlace(t *testing.T, s *Store, parent *Place, kind, number, name string, tags ...string) *Place {
	t.Helper()
	p := &Place{Kind: kind, Number: number, Name: name, Tags: tags}
	if parent != nil {
		p.ParentID = parent.ID
	}
	must(t, s.SavePlace(p))
	return p
}

// building adds a site and a building in it.
func building(t *testing.T, s *Store, name string) *Place {
	t.Helper()
	return addPlace(t, s, addPlace(t, s, nil, KindSite, "", "Campus"), KindBuilding, "", name)
}

func TestPlaceTree(t *testing.T) {
	s := openTest(t)
	site := addPlace(t, s, nil, KindSite, "", "Grace Church")
	main := addPlace(t, s, site, KindBuilding, "", "Main")
	upstairs := addPlace(t, s, main, KindFloor, "", "2nd floor")
	kids := addPlace(t, s, upstairs, KindArea, "", "Classrooms", "classrooms")
	for _, r := range []Place{{Name: "Attic"}, {Number: "10", Name: "Office"}, {Number: " 9 ", Name: "Nursery"}} {
		addPlace(t, s, kids, KindRoom, r.Number, r.Name)
	}
	porch := addPlace(t, s, main, KindArea, "", "Porch")
	booth := addPlace(t, s, addPlace(t, s, main, KindRoom, "", "Sanctuary", "media"), KindArea, "", "Sound booth", "Media")

	tree, err := s.Places()
	must(t, err)
	var got []string
	for _, p := range tree.All() {
		got = append(got, p.Path)
	}
	// One site is left out of paths; floors come before rooms, rooms before
	// areas; numbered rooms come first, in number order.
	want := []string{"Grace Church", "Main", "Main › 2nd floor", "Main › 2nd floor › Classrooms",
		"Main › 2nd floor › Classrooms › 9 – Nursery", "Main › 2nd floor › Classrooms › 10 – Office", "Main › 2nd floor › Classrooms › Attic",
		"Main › Sanctuary", "Main › Sanctuary › Sound booth", "Main › Porch"}
	if !slices.Equal(got, want) {
		t.Fatalf("paths =\n%q\nwant\n%q", got, want)
	}
	if top := tree.Top(); len(top) != 1 || top[0].ID != main.ID {
		t.Errorf("top = %+v", top)
	}
	if b, _ := tree.Building(booth.ID); b.ID != main.ID {
		t.Errorf("building of booth = %+v", b)
	}
	if !tree.Within(booth.ID, main.ID) || tree.Within(main.ID, booth.ID) || !tree.Within(porch.ID, porch.ID) {
		t.Error("Within is wrong")
	}
	if !tree.CanHold(main.ID, KindArea) || !tree.CanHold(kids.ID, KindFloor) || tree.CanHold(booth.ID, KindBuilding) || tree.CanHold(site.ID, KindRoom) ||
		tree.CanHold(upstairs.ID, KindFloor) || tree.CanHold(0, KindBuilding) || !tree.CanHold(0, KindSite) || !tree.CanHold(site.ID, KindArea) {
		t.Error("CanHold is wrong")
	}
	if tags := tree.Tags(); len(tags) != 2 || tags[0] != (TagCount{"classrooms", 1}) || tags[1] != (TagCount{"media", 2}) {
		t.Errorf("tags = %+v", tags)
	}

	// Things anywhere inside a place show up for it; tags reach inside too.
	item := &Item{PlaceID: booth.ID, Name: "Mixing console"}
	must(t, s.SaveItem(item, nil, 0))
	must(t, s.CreateProblem(&Problem{PlaceID: booth.ID, Title: "Hum"}))
	for _, f := range []ItemFilter{{PlaceID: main.ID}, {PlaceID: site.ID}, {Tag: "MEDIA"}, {PlaceID: booth.ID, Direct: true}} {
		if items, _ := s.ListItems(f); len(items) != 1 || items[0].Location() != "Main › Sanctuary › Sound booth" {
			t.Errorf("%+v: items = %+v", f, items)
		}
	}
	for _, f := range []ItemFilter{{PlaceID: main.ID, Direct: true}, {PlaceID: porch.ID}, {Tag: "classrooms"}} {
		if items, _ := s.ListItems(f); len(items) != 0 {
			t.Errorf("%+v: items = %+v", f, items)
		}
	}
	if ps, _ := s.ListProblems(ProblemFilter{PlaceID: main.ID}); len(ps) != 1 {
		t.Errorf("problems in main = %d", len(ps))
	}
	tree, _ = s.Places()
	if p, _ := tree.Get(main.ID); p.ItemTotal != 1 || p.ItemCount != 0 || p.ProblemTotal != 1 || p.ChildCount != 3 {
		t.Errorf("main counts = %+v", p)
	}

	// A second site shows up in paths.
	addPlace(t, s, nil, KindSite, "", "North campus")
	tree, _ = s.Places()
	if got := tree.Path(booth.ID); got != "Grace Church › Main › Sanctuary › Sound booth" {
		t.Errorf("path with two sites = %q", got)
	}
}

func TestParseTags(t *testing.T) {
	if got := ParseTags(" Classrooms,  kids   wing ,, classrooms,media"); !slices.Equal(got, []string{"Classrooms", "kids wing", "media"}) {
		t.Errorf("tags = %q", got)
	}
}

func TestDeletePlaceMovesThingsUp(t *testing.T) {
	s := openTest(t)
	main := building(t, s, "Main")
	zone := addPlace(t, s, main, KindArea, "", "East wing", "kids")
	room := addPlace(t, s, zone, KindRoom, "", "Nursery")
	item := &Item{PlaceID: zone.ID, Name: "Exit sign"}
	must(t, s.SaveItem(item, nil, 0))
	sp := &Supply{PlaceID: zone.ID, Name: "Bulbs"}
	must(t, s.SaveSupply(sp, 0))
	p := &Problem{PlaceID: zone.ID, Title: "Dark"}
	must(t, s.CreateProblem(p))

	must(t, s.DeletePlace(zone.ID))
	if got, _ := s.GetPlace(room.ID); got.ParentID != main.ID {
		t.Errorf("room parent = %d", got.ParentID)
	}
	if got, _ := s.GetItem(item.ID); got.PlaceID != main.ID {
		t.Errorf("item place = %d", got.PlaceID)
	}
	if got, _ := s.GetSupply(sp.ID); got.PlaceID != main.ID {
		t.Errorf("supply place = %d", got.PlaceID)
	}
	if got, _ := s.GetProblem(p.ID); got.PlaceID != main.ID {
		t.Errorf("problem place = %d", got.PlaceID)
	}
	var tags int
	s.DB.QueryRow(`SELECT COUNT(*) FROM tags`).Scan(&tags)
	if tags != 0 {
		t.Error("unused tag should be removed")
	}
	// A site with things in it can't be deleted; an empty one can.
	if err := s.DeletePlace(main.ParentID); !errors.Is(err, ErrPlaceNotEmpty) {
		t.Errorf("err = %v", err)
	}
	empty := addPlace(t, s, nil, KindSite, "", "Empty")
	must(t, s.DeletePlace(empty.ID))
}

func TestEnsureSite(t *testing.T) {
	s := openTest(t)
	id, err := s.EnsureSite("Grace Church")
	must(t, err)
	if again, _ := s.EnsureSite("Other"); again != id || id == 0 {
		t.Errorf("EnsureSite = %d then %d", id, again)
	}
	addPlace(t, s, nil, KindSite, "", "North")
	if id, _ := s.EnsureSite("x"); id != 0 {
		t.Errorf("with two sites EnsureSite = %d, want 0", id)
	}
}

// TestPlacesMigration upgrades a database with buildings and rooms.
func TestPlacesMigration(t *testing.T) {
	s, err := openAt(filepath.Join(t.TempDir(), "v9.db"), 9) // just before places
	must(t, err)
	defer s.Close()
	for _, q := range []string{
		`UPDATE settings SET value = 'Grace Church' WHERE key = 'site_name'`,
		`INSERT INTO buildings (id, name, address) VALUES (1, 'Sanctuary', '1 Main St'), (2, 'Parsonage', '')`,
		`INSERT INTO rooms (id, building_id, number, name, floor) VALUES (1, 1, '104', 'Nursery', '1st floor'), (2, 1, '', 'Office', '1st floor'), (3, 1, 'B1', 'Boiler', ''), (4, 2, '', 'Kitchen', '')`,
		`INSERT INTO supplies (id, building_id, room_id, name, quantity) VALUES (1, 1, 1, 'Diapers', 4), (2, 2, NULL, 'Bulbs', 3)`,
		`INSERT INTO items (id, building_id, room_id, name, supply_id, quantity) VALUES (1, 1, 1, 'Lights', 2, 6), (2, 1, NULL, 'Roof', NULL, 1)`,
		`INSERT INTO tasks (id, item_id, name) VALUES (1, 2, 'Inspect')`,
		`INSERT INTO maintenance_logs (id, item_id, task_id, performed_on) VALUES (1, 2, 1, '2026-01-01')`,
		`INSERT INTO item_units (item_id, number, label) VALUES (1, 3, 'By the door')`,
		`INSERT INTO problems (id, building_id, room_id, item_id, unit, title) VALUES (1, 1, 1, 1, 3, 'Light out'), (2, 2, NULL, NULL, NULL, 'Leak')`,
		`INSERT INTO supply_changes (supply_id, kind, delta, quantity_after) VALUES (1, 'added', 4, 4)`,
	} {
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(t, s.migrate(0))

	tree, err := s.Places()
	must(t, err)
	var got []string
	for _, p := range tree.All() {
		got = append(got, p.KindLabel()+": "+p.Path)
	}
	want := []string{"Site: Grace Church", "Building: Parsonage", "Room: Parsonage › Kitchen", "Building: Sanctuary",
		"Floor: Sanctuary › 1st floor", "Room: Sanctuary › 1st floor › 104 – Nursery", "Room: Sanctuary › 1st floor › Office", "Room: Sanctuary › B1 – Boiler"}
	if !slices.Equal(got, want) {
		t.Fatalf("places =\n%q\nwant\n%q", got, want)
	}
	if it, _ := s.GetItem(1); it.Location() != "Sanctuary › 1st floor › 104 – Nursery" || it.Supply.Location() != "Parsonage" || it.Quantity != 6 {
		t.Errorf("lights = %+v", it)
	}
	if it, _ := s.GetItem(2); it.Location() != "Sanctuary" || it.NextDue != "" {
		t.Errorf("roof = %+v", it)
	}
	if p, _ := s.GetProblem(1); p.Location() != "Sanctuary › 1st floor › 104 – Nursery" || p.WhatLabel() != "Lights #3 – By the door" {
		t.Errorf("problem = %+v", p)
	}
	if p, _ := s.GetProblem(2); p.Location() != "Parsonage" {
		t.Errorf("problem 2 = %+v", p)
	}
	if logs, _ := s.ListLogs(LogFilter{ItemID: 2}); len(logs) != 1 || logs[0].TaskName != "Inspect" {
		t.Errorf("logs = %+v", logs)
	}
	// Old links still find their place.
	if id, _ := s.LegacyPlace(1, 0); tree.Path(id) != "Sanctuary" {
		t.Errorf("building 1 = %d", id)
	} else if b, _ := tree.Get(id); b.Address != "1 Main St" || b.Kind != KindBuilding {
		t.Errorf("sanctuary = %+v", b)
	}
	if id, _ := s.LegacyPlace(0, 1); tree.Path(id) != "Sanctuary › 1st floor › 104 – Nursery" {
		t.Errorf("room 1 = %d", id)
	}
	if id, _ := s.LegacyPlace(2, 0); tree.Path(id) != "Parsonage" {
		t.Errorf("building 2 = %d", id)
	}
	// Foreign keys are back on: deleting an item still cascades.
	must(t, s.DeleteItem(1))
	var units int
	s.DB.QueryRow(`SELECT COUNT(*) FROM item_units`).Scan(&units)
	if units != 0 {
		t.Error("foreign keys should be on after the migration")
	}
}

func TestRecordMaintenanceRollsTaskForward(t *testing.T) {
	s := openTest(t)
	b := building(t, s, "Sanctuary")
	room := addPlace(t, s, b, KindRoom, "", "Boiler room")
	item := &Item{PlaceID: room.ID, Name: "Furnace"}
	must(t, s.SaveItem(item, nil, 0))

	recurring := &Task{ItemID: item.ID, Name: "Filter", IntervalValue: 3, IntervalUnit: "months", NextDueOn: "2026-09-01", Active: true}
	must(t, s.SaveTask(recurring))
	oneOff := &Task{ItemID: item.ID, Name: "Inspect flue", NextDueOn: "2026-10-01", Active: true}
	must(t, s.SaveTask(oneOff))

	must(t, s.RecordMaintenance(Completion{ItemID: item.ID, TaskID: recurring.ID, PerformedOn: "2026-09-15", CostCents: 2599}))
	got, _ := s.GetTask(recurring.ID)
	if got.LastCompletedOn != "2026-09-15" || got.NextDueOn != "2026-12-15" || !got.Active {
		t.Fatalf("recurring task after completion: %+v", got)
	}

	// A back-dated entry must not move the schedule backwards.
	must(t, s.RecordMaintenance(Completion{ItemID: item.ID, TaskID: recurring.ID, PerformedOn: "2026-06-01"}))
	got, _ = s.GetTask(recurring.ID)
	if got.LastCompletedOn != "2026-09-15" || got.NextDueOn != "2026-12-15" {
		t.Fatalf("back-dated completion changed schedule: %+v", got)
	}

	must(t, s.RecordMaintenance(Completion{ItemID: item.ID, TaskID: oneOff.ID, PerformedOn: "2026-09-20"}))
	got, _ = s.GetTask(oneOff.ID)
	if got.Active || got.NextDueOn != "" {
		t.Fatalf("one-off task should be closed: %+v", got)
	}

	logs, err := s.ListLogs(LogFilter{ItemID: item.ID})
	must(t, err)
	if len(logs) != 3 || logs[0].Cost() != "" || logs[1].Cost() != "$25.99" {
		t.Fatalf("unexpected logs: %+v", logs)
	}

	items, err := s.ListItems(ItemFilter{PlaceID: b.ID})
	must(t, err)
	if len(items) != 1 || items[0].NextDue != "2026-12-15" {
		t.Fatalf("item next due: %+v", items)
	}

	// Deleting the room keeps the item at building level.
	must(t, s.DeletePlace(room.ID))
	it, err := s.GetItem(item.ID)
	must(t, err)
	if it.PlaceID != b.ID {
		t.Fatalf("item after room delete: %+v", it)
	}
}

func TestSessions(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateUser("admin", "Admin", "correct horse battery", true)
	must(t, err)
	if _, ok := s.Authenticate("ADMIN", "correct horse battery"); !ok {
		t.Fatal("username should be case-insensitive")
	}
	if _, ok := s.Authenticate("admin", "wrong"); ok {
		t.Fatal("bad password accepted")
	}
	sess, err := s.CreateSession(id, time.Hour)
	must(t, err)
	if _, err := s.GetSession(sess.Token); err != nil {
		t.Fatal(err)
	}
	must(t, s.SetPassword(id, "another long password"))
	if _, err := s.GetSession(sess.Token); err != ErrNotFound {
		t.Fatal("password change should revoke sessions")
	}
	expired, err := s.CreateSession(id, -time.Minute)
	must(t, err)
	if _, err := s.GetSession(expired.Token); err != ErrNotFound {
		t.Fatal("expired session accepted")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestAdjustSupply(t *testing.T) {
	s := openTest(t)
	b := building(t, s, "Sanctuary")
	room := addPlace(t, s, b, KindRoom, "", "Kitchen")
	sp := &Supply{PlaceID: room.ID, Name: "Paper towels", Unit: "rolls", Quantity: 5, ReorderAt: 2}
	must(t, s.SaveSupply(sp, 0))

	steps := []struct {
		kind    string
		amount  int
		want    int
		wantErr error
	}{
		{"used", 2, 3, nil},
		{"used", 4, 3, ErrNotEnough}, // can't go below zero; nothing changes
		{"restocked", 10, 13, nil},
		{"counted", 1, 1, nil},
	}
	for _, st := range steps {
		err := s.AdjustSupply(Adjustment{SupplyID: sp.ID, Kind: st.kind, Amount: st.amount})
		got, _ := s.GetSupply(sp.ID)
		if !errors.Is(err, st.wantErr) || got.Quantity != st.want {
			t.Fatalf("%s %d: got %d, %v; want %d, %v", st.kind, st.amount, got.Quantity, err, st.want, st.wantErr)
		}
	}
	if err := s.AdjustSupply(Adjustment{SupplyID: sp.ID, Kind: "swapped", Amount: 1}); err == nil {
		t.Fatal("reusable-only adjustments should be rejected for consumables")
	}

	got, err := s.GetSupply(sp.ID)
	must(t, err)
	if got.Quantity != 1 || got.Stock() != "low" || got.QuantityLabel() != "1 rolls" {
		t.Fatalf("supply = %+v (stock %s)", got, got.Stock())
	}
	low, err := s.ListSupplies(SupplyFilter{LowOnly: true})
	must(t, err)
	if len(low) != 1 {
		t.Fatalf("expected supply in low-stock list, got %d", len(low))
	}

	changes, err := s.ListSupplyChanges(sp.ID)
	must(t, err)
	var deltas []int
	for _, c := range changes {
		deltas = append(deltas, c.Delta)
	}
	if want := []int{-12, 10, -2, 5}; !slices.Equal(deltas, want) {
		t.Fatalf("history deltas = %v, want %v", deltas, want)
	}

	// Deleting the room keeps the supply, building-wide.
	must(t, s.DeletePlace(room.ID))
	if got, _ := s.GetSupply(sp.ID); got.PlaceID != b.ID {
		t.Fatalf("supply after room delete: %+v", got)
	}
}

func TestReusableSupply(t *testing.T) {
	s := openTest(t)
	b := building(t, s, "Sanctuary")
	sp := &Supply{PlaceID: b.ID, Name: "Mop heads", Reusable: true, Quantity: 4, InUse: 2, ReorderAt: 1}
	must(t, s.SaveSupply(sp, 0))

	type counts struct{ clean, inUse, cleaning int }
	steps := []struct {
		kind, from string
		amount     int
		want       counts
		wantErr    string
	}{
		{"swapped", "", 2, counts{2, 2, 2}, ""}, // two dirty off, two clean on
		{"sent_cleaning", "", 3, counts{2, 2, 2}, "only 2 in use"},
		{"returned", "", 1, counts{3, 2, 1}, ""},
		{"put_in_use", "", 1, counts{2, 3, 1}, ""},
		{"sent_cleaning", "", 3, counts{2, 0, 4}, ""},
		{"swapped", "", 1, counts{2, 0, 4}, "only 0 in use"},
		{"retired", "cleaning", 1, counts{2, 0, 3}, ""}, // worn out at the laundry
		{"restocked", "", 2, counts{4, 0, 3}, ""},
		{"returned", "", 4, counts{4, 0, 3}, "only 3 out for cleaning"},
	}
	for _, st := range steps {
		err := s.AdjustSupply(Adjustment{SupplyID: sp.ID, Kind: st.kind, From: st.from, Amount: st.amount})
		if st.wantErr == "" && err != nil || st.wantErr != "" && (err == nil || err.Error() != st.wantErr || !errors.Is(err, ErrNotEnough)) {
			t.Fatalf("%s %d: err = %v, want %q", st.kind, st.amount, err, st.wantErr)
		}
		got, _ := s.GetSupply(sp.ID)
		if c := (counts{got.Quantity, got.InUse, got.Cleaning}); c != st.want {
			t.Fatalf("%s %d: counts = %+v, want %+v", st.kind, st.amount, c, st.want)
		}
	}
	got, _ := s.GetSupply(sp.ID)
	if got.Total() != 7 || got.StockSummary() != "4 clean · 3 cleaning" {
		t.Fatalf("total %d, summary %q", got.Total(), got.StockSummary())
	}

	// Can't switch to consumable while some are out for cleaning.
	got.Reusable = false
	if err := s.SaveSupply(got, 0); !errors.Is(err, ErrReusableInCirculation) {
		t.Fatalf("expected ErrReusableInCirculation, got %v", err)
	}
	must(t, s.AdjustSupply(Adjustment{SupplyID: sp.ID, Kind: "returned", Amount: 3}))
	must(t, s.SaveSupply(got, 0))
	if got, _ := s.GetSupply(sp.ID); got.Reusable || got.Quantity != 7 {
		t.Fatalf("after switching to consumable: %+v", got)
	}
}

func TestTaskUsesSupply(t *testing.T) {
	s := openTest(t)
	b := building(t, s, "Sanctuary")
	item := &Item{PlaceID: b.ID, Name: "Air return"}
	must(t, s.SaveItem(item, nil, 0))
	filters := &Supply{PlaceID: b.ID, Name: "Filters 20x25x1", Quantity: 3, ReorderAt: 1}
	must(t, s.SaveSupply(filters, 0))
	task := &Task{ItemID: item.ID, Name: "Replace filter", IntervalValue: 3, IntervalUnit: "months", Active: true, SupplyID: filters.ID, SupplyAmount: 2}
	must(t, s.SaveTask(task))

	got, err := s.GetTask(task.ID)
	must(t, err)
	if got.Supply.Name != "Filters 20x25x1" || got.Supply.Quantity != 3 || got.SupplyShort() {
		t.Fatalf("task supply = %+v", got.Supply)
	}

	done := Completion{ItemID: item.ID, TaskID: task.ID, PerformedOn: "2026-01-01", SupplyID: filters.ID, SupplyAmount: 2}
	must(t, s.RecordMaintenance(done))
	if sp, _ := s.GetSupply(filters.ID); sp.Quantity != 1 {
		t.Fatalf("quantity after use = %d, want 1", sp.Quantity)
	}
	logs, err := s.ListLogs(LogFilter{ItemID: item.ID})
	must(t, err)
	if len(logs) != 1 || logs[0].SupplyUsed != "2 × Filters 20x25x1" {
		t.Fatalf("logs = %+v", logs)
	}

	// Not enough left: nothing is recorded and the task isn't rolled forward.
	done.PerformedOn = "2026-04-01"
	if err := s.RecordMaintenance(done); !errors.Is(err, ErrNotEnough) {
		t.Fatalf("expected ErrNotEnough, got %v", err)
	}
	if logs, _ := s.ListLogs(LogFilter{ItemID: item.ID}); len(logs) != 1 {
		t.Fatalf("failed record should not add history, got %d entries", len(logs))
	}
	if got, _ := s.GetTask(task.ID); got.LastCompletedOn != "2026-01-01" {
		t.Fatalf("failed record moved the task: %+v", got)
	}

	// Deleting the history entry puts the filters back.
	_, err = s.DeleteLog(logs[0].ID, 0)
	must(t, err)
	if sp, _ := s.GetSupply(filters.ID); sp.Quantity != 3 {
		t.Fatalf("quantity after deleting history = %d, want 3", sp.Quantity)
	}

	if ts, _ := s.ListTasks(TaskFilter{SupplyID: filters.ID}); len(ts) != 1 {
		t.Fatalf("tasks using supply = %d, want 1", len(ts))
	}
	// Deleting the supply unlinks the task but keeps it.
	must(t, s.DeleteSupply(filters.ID))
	if got, _ := s.GetTask(task.ID); got.SupplyID != 0 {
		t.Fatalf("task still linked to deleted supply: %+v", got)
	}
}

func TestPlaceMoveCarriesContents(t *testing.T) {
	s := openTest(t)
	a := building(t, s, "A")
	b := addPlace(t, s, &Place{ID: a.ParentID}, KindBuilding, "", "B")
	room := addPlace(t, s, a, KindRoom, "", "Hall")
	p := &Problem{PlaceID: room.ID, Title: "Flickering"}
	must(t, s.CreateProblem(p))

	room.ParentID = b.ID
	must(t, s.SavePlace(room))
	if ps, _ := s.ListProblems(ProblemFilter{PlaceID: b.ID}); len(ps) != 1 || ps[0].Location() != "B › Hall" {
		t.Errorf("problems in B = %+v", ps)
	}
}

func TestMoveItem(t *testing.T) {
	s := openTest(t)
	b := building(t, s, "Main")
	r1, r2 := addPlace(t, s, b, KindRoom, "104", "Nursery"), addPlace(t, s, b, KindRoom, "", "Gym")
	item := &Item{PlaceID: r1.ID, Name: "Projector", Portable: true}
	must(t, s.SaveItem(item, nil, 0))

	// Moving to where it already is records nothing.
	must(t, s.MoveItem(Move{ItemID: item.ID, PlaceID: r1.ID, MovedOn: "2026-01-01"}))
	must(t, s.MoveItem(Move{ItemID: item.ID, PlaceID: r2.ID, MovedOn: "2026-01-02", Note: "For the retreat"}))
	if got, _ := s.GetItem(item.ID); got.PlaceID != r2.ID {
		t.Errorf("item in place %d, want %d", got.PlaceID, r2.ID)
	}
	logs, err := s.ListLogs(LogFilter{ItemID: item.ID})
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs = %+v, %v", logs, err)
	}
	if l := logs[0]; l.Kind != "moved" || l.Notes != "Moved from Main › 104 – Nursery to Main › Gym.\nFor the retreat" {
		t.Errorf("move log = %+v", l)
	}
	// A place that doesn't exist is refused.
	if err := s.MoveItem(Move{ItemID: item.ID, PlaceID: 999, MovedOn: "2026-01-03"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestSharedArea: a stairwell that lives on the 1st floor and is also on
// the 2nd floor and off two rooms.
func TestSharedArea(t *testing.T) {
	s := openTest(t)
	main := building(t, s, "Main")
	first, second := addPlace(t, s, main, KindFloor, "", "1st floor"), addPlace(t, s, main, KindFloor, "", "2nd floor")
	roomA, roomB := addPlace(t, s, first, KindRoom, "", "A"), addPlace(t, s, second, KindRoom, "", "B")
	stairs := &Place{ParentID: first.ID, Kind: KindArea, Name: "Stairwell", AlsoIn: []int64{second.ID, roomA.ID, roomB.ID, first.ID, second.ID}}
	must(t, s.SavePlace(stairs))
	item := &Item{PlaceID: stairs.ID, Name: "Stair lights", Quantity: 3}
	must(t, s.SaveItem(item, nil, 0))
	must(t, s.CreateProblem(&Problem{PlaceID: stairs.ID, Title: "Bulb out"}))

	tree, err := s.Places()
	must(t, err)
	p, _ := tree.Get(stairs.ID)
	// Its own place is never also a link; repeats collapse.
	if p.Path != "Main › 1st floor › Stairwell" || len(p.AlsoIn) != 3 || !p.Shared() {
		t.Fatalf("stairwell = %+v", p)
	}
	for _, in := range []*Place{first, second, roomA, roomB, main} {
		if !tree.Within(stairs.ID, in.ID) {
			t.Errorf("stairwell should be within %s", in.Name)
		}
		if items, _ := s.ListItems(ItemFilter{PlaceID: in.ID}); len(items) != 1 {
			t.Errorf("items in %s = %d", in.Name, len(items))
		}
		if ps, _ := s.ListProblems(ProblemFilter{PlaceID: in.ID}); len(ps) != 1 {
			t.Errorf("problems in %s = %d", in.Name, len(ps))
		}
		if got, _ := tree.Get(in.ID); got.ItemTotal != 1 || got.ProblemTotal != 1 {
			t.Errorf("%s totals = %d items, %d problems; the stairwell should count once", in.Name, got.ItemTotal, got.ProblemTotal)
		}
	}
	if kids := tree.Children(second.ID); len(kids) != 2 || kids[1].ID != stairs.ID || kids[1].ParentID != first.ID {
		t.Errorf("2nd floor children = %+v", kids)
	}
	if len(tree.Inside(main.ID)) != 5 {
		t.Errorf("inside main = %d places, want 5 (each once)", len(tree.Inside(main.ID)))
	}

	// Deleting a place it's only linked to drops the link.
	must(t, s.DeletePlace(roomB.ID))
	if got, _ := s.GetPlace(stairs.ID); len(got.AlsoIn) != 2 {
		t.Errorf("after deleting B, also in %v", got.AlsoIn)
	}
	// Deleting its home moves it up; a link to where it lands goes away.
	stairs.AlsoIn = []int64{second.ID, main.ID}
	must(t, s.SavePlace(stairs))
	must(t, s.DeletePlace(first.ID))
	got, _ := s.GetPlace(stairs.ID)
	if got.ParentID != main.ID || !slices.Equal(got.AlsoIn, []int64{second.ID}) {
		t.Errorf("after deleting its floor: parent %d, also in %v", got.ParentID, got.AlsoIn)
	}
	// Turning it into a room drops the links: only areas are shared.
	got.Kind = KindRoom
	must(t, s.SavePlace(got))
	if got, _ := s.GetPlace(stairs.ID); got.Shared() {
		t.Errorf("a room can't be shared: %v", got.AlsoIn)
	}
}

// TestDeleteStuck: a room in an area on the site has nowhere to go if the
// area is deleted, since rooms don't go straight on a site.
func TestDeleteStuck(t *testing.T) {
	s := openTest(t)
	site := addPlace(t, s, nil, KindSite, "", "Campus")
	grounds := addPlace(t, s, site, KindArea, "", "Grounds")
	addPlace(t, s, grounds, KindRoom, "", "Shed")
	var stuck *StuckError
	if err := s.DeletePlace(grounds.ID); !errors.As(err, &stuck) || stuck.Place.Name != "Shed" || stuck.Into.ID != site.ID {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.GetPlace(grounds.ID); err != nil {
		t.Error("nothing should change when deleting gets stuck")
	}
}

// TestZonesBecomeAreas upgrades a database that has zones.
func TestZonesBecomeAreas(t *testing.T) {
	s, err := openAt(filepath.Join(t.TempDir(), "v10.db"), 10)
	must(t, err)
	defer s.Close()
	for _, q := range []string{
		`INSERT INTO places (id, parent_id, kind, name) VALUES (1, NULL, 'site', 'Campus'), (2, 1, 'building', 'Main'), (3, 2, 'zone', 'East wing'), (4, 3, 'room', 'Nursery')`,
		`INSERT INTO items (place_id, name) VALUES (3, 'Exit sign')`,
	} {
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(t, s.migrate(0))
	tree, _ := s.Places()
	if p, _ := tree.Get(3); p.Kind != KindArea || p.Path != "Main › East wing" || p.ItemCount != 1 {
		t.Errorf("east wing = %+v", p)
	}
	if p, _ := tree.Get(4); p.Path != "Main › East wing › Nursery" {
		t.Errorf("nursery = %+v", p)
	}
}
