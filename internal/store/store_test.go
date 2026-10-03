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

func TestListRoomsOrdersByNumber(t *testing.T) {
	s := openTest(t)
	b := &Building{Name: "Sanctuary"}
	must(t, s.SaveBuilding(b))
	for _, r := range []Room{{Name: "Attic"}, {Number: "10", Name: "Office"}, {Number: " 9 ", Name: "Nursery"}} {
		r.BuildingID = b.ID
		must(t, s.SaveRoom(&r))
	}
	rooms, err := s.ListRooms(b.ID)
	must(t, err)
	var got []string
	for _, r := range rooms {
		got = append(got, r.Label())
	}
	if want := []string{"9 – Nursery", "10 – Office", "Attic"}; !slices.Equal(got, want) {
		t.Fatalf("rooms = %q, want %q", got, want)
	}
}

func TestRecordMaintenanceRollsTaskForward(t *testing.T) {
	s := openTest(t)
	b := &Building{Name: "Sanctuary"}
	must(t, s.SaveBuilding(b))
	room := &Room{BuildingID: b.ID, Name: "Boiler room"}
	must(t, s.SaveRoom(room))
	item := &Item{BuildingID: b.ID, RoomID: room.ID, Name: "Furnace"}
	must(t, s.SaveItem(item))

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

	items, err := s.ListItems(ItemFilter{BuildingID: b.ID})
	must(t, err)
	if len(items) != 1 || items[0].NextDue != "2026-12-15" {
		t.Fatalf("item next due: %+v", items)
	}

	// Deleting the room keeps the item at building level.
	must(t, s.DeleteRoom(room.ID))
	it, err := s.GetItem(item.ID)
	must(t, err)
	if it.RoomID != 0 || it.BuildingID != b.ID {
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
	b := &Building{Name: "Sanctuary"}
	must(t, s.SaveBuilding(b))
	room := &Room{BuildingID: b.ID, Name: "Kitchen"}
	must(t, s.SaveRoom(room))
	sp := &Supply{BuildingID: b.ID, RoomID: room.ID, Name: "Paper towels", Unit: "rolls", Quantity: 5, ReorderAt: 2}
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
	must(t, s.DeleteRoom(room.ID))
	if got, _ := s.GetSupply(sp.ID); got.RoomID != 0 || got.BuildingID != b.ID {
		t.Fatalf("supply after room delete: %+v", got)
	}
}

func TestReusableSupply(t *testing.T) {
	s := openTest(t)
	b := &Building{Name: "Sanctuary"}
	must(t, s.SaveBuilding(b))
	sp := &Supply{BuildingID: b.ID, Name: "Mop heads", Reusable: true, Quantity: 4, InUse: 2, ReorderAt: 1}
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
	b := &Building{Name: "Sanctuary"}
	must(t, s.SaveBuilding(b))
	item := &Item{BuildingID: b.ID, Name: "Air return"}
	must(t, s.SaveItem(item))
	filters := &Supply{BuildingID: b.ID, Name: "Filters 20x25x1", Quantity: 3, ReorderAt: 1}
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
