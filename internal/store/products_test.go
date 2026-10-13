package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func unitRows(t *testing.T, s *Store, itemID int64) int {
	t.Helper()
	var n int
	must(t, s.DB.QueryRow(`SELECT COUNT(*) FROM item_units WHERE item_id = ?`, itemID).Scan(&n))
	return n
}

// TestProducts: folding chairs in two rooms add up, get switched to only
// being counted, move by count, and are switched back.
func TestProducts(t *testing.T) {
	s := openTest(t)
	b := building(t, s, "Main")
	hall, gym := addPlace(t, s, b, KindRoom, "", "Hall"), addPlace(t, s, b, KindRoom, "", "Gym")
	hallChairs := &Item{PlaceID: hall.ID, Name: "Folding chairs", Category: "Furniture", Quantity: 40, Portable: true}
	must(t, s.SaveItem(hallChairs, nil, 0))
	// Same name, any case: the same product, whose category stands.
	gymChairs := &Item{PlaceID: gym.ID, Name: "folding chairs", Category: "Seating", Quantity: 60, Portable: true}
	must(t, s.SaveItem(gymChairs, nil, 0))
	if gymChairs.ProductID != hallChairs.ProductID || gymChairs.Name != "Folding chairs" || gymChairs.Category != "Furniture" {
		t.Fatalf("gym chairs = %+v", gymChairs)
	}
	ps, err := s.ListProducts(ProductFilter{})
	must(t, err)
	if len(ps) != 1 || ps[0].Total != 100 || ps[0].ItemCount != 2 || ps[0].PlaceCount != 2 {
		t.Fatalf("products = %+v", ps)
	}
	if ps, _ := s.ListProducts(ProductFilter{PlaceID: gym.ID}); len(ps) != 1 || ps[0].Total != 60 {
		t.Errorf("products in the gym = %+v", ps)
	}
	if ps, _ := s.ListProducts(ProductFilter{Category: "furniture", Query: "fold"}); len(ps) != 1 {
		t.Errorf("products by category = %+v", ps)
	}
	if ps, _ := s.ListProducts(ProductFilter{CountedOnly: true}); len(ps) != 0 {
		t.Errorf("counted products = %+v", ps)
	}

	// Portable units share one list of IDs: the gym's carry on from 41.
	if units, _ := s.ItemUnits(gymChairs.ID, 60, time.Now()); units[0].ID() != "41" {
		t.Errorf("gym unit #1 = %+v", units[0])
	}
	must(t, s.RecordMaintenance(Completion{ItemID: hallChairs.ID, PerformedOn: "2026-01-05", Units: []int{2}}))
	p := &Problem{PlaceID: hall.ID, ItemID: hallChairs.ID, Title: "Wobbly"}
	must(t, s.CreateProblem(p))
	must(t, s.SetProblemUnit(p.ID, 3, 0))

	// Only counted: no units, history and problems forget which one.
	prod, err := s.GetProduct(hallChairs.ProductID)
	must(t, err)
	prod.Counted, prod.Notes = true, "Stacks 10 high"
	must(t, s.SaveProduct(prod))
	if n := unitRows(t, s, hallChairs.ID) + unitRows(t, s, gymChairs.ID); n != 0 {
		t.Errorf("%d unit rows left on counted items", n)
	}
	if logs, _ := s.ListLogs(LogFilter{ItemID: hallChairs.ID}); len(logs) != 1 || logs[0].UnitsLabel() != "" {
		t.Errorf("logs = %+v", logs)
	}
	if got, _ := s.GetProblem(p.ID); got.Unit != 0 {
		t.Errorf("problem unit = %d", got.Unit)
	}
	if err := s.SetProblemUnit(p.ID, 1, 0); !errors.Is(err, ErrCounted) {
		t.Errorf("SetProblemUnit err = %v, want ErrCounted", err)
	}
	if err := s.RecordMaintenance(Completion{ItemID: hallChairs.ID, PerformedOn: "2026-01-06", Units: []int{1}}); !errors.Is(err, ErrCounted) {
		t.Errorf("RecordMaintenance err = %v, want ErrCounted", err)
	}
	if got, _ := s.GetItem(gymChairs.ID); !got.Counted || got.HasUnits() {
		t.Errorf("gym chairs = %+v", got)
	}

	// Counted ones move by count, and join the chairs already there.
	into, err := s.MoveItem(Move{ItemID: gymChairs.ID, PlaceID: hall.ID, Count: 10, Units: []int{1, 2}, MovedOn: "2026-01-10"})
	must(t, err)
	if into != hallChairs.ID {
		t.Errorf("moved into %d, want %d", into, hallChairs.ID)
	}
	if got, _ := s.GetItem(hallChairs.ID); got.Quantity != 50 {
		t.Errorf("hall chairs = %d, want 50", got.Quantity)
	}
	if n := unitRows(t, s, hallChairs.ID) + unitRows(t, s, gymChairs.ID); n != 0 {
		t.Errorf("moving made %d unit rows", n)
	}
	if logs, _ := s.ListLogs(LogFilter{ItemID: gymChairs.ID}); logs[0].Notes != "Moved 10 of 60 from Main › Gym to Main › Hall." {
		t.Errorf("gym move note = %q", logs[0].Notes)
	}
	if logs, _ := s.ListLogs(LogFilter{ItemID: hallChairs.ID}); logs[0].Notes != "Moved 10 from Main › Gym to Main › Hall." {
		t.Errorf("hall move note = %q", logs[0].Notes)
	}

	// Back to one by one: numbered afresh, sharing one list again.
	prod.Counted = false
	must(t, s.SaveProduct(prod))
	if units, _ := s.ItemUnits(gymChairs.ID, 50, time.Now()); units[0].ID() != "51" || unitRows(t, s, gymChairs.ID) != 50 {
		t.Errorf("gym unit #1 after = %+v", units[0])
	}

	// Renaming to another product's name is refused; merging works,
	// giving clashing IDs new ones.
	odd := &Item{PlaceID: hall.ID, Name: "Chairs, folding", Category: "", Quantity: 2, Portable: true}
	must(t, s.SaveItem(odd, nil, 0))
	other, _ := s.GetProduct(odd.ProductID)
	other.Name = "FOLDING CHAIRS"
	if err := s.SaveProduct(other); !errors.Is(err, ErrProductExists) {
		t.Errorf("rename err = %v, want ErrProductExists", err)
	}
	must(t, s.MergeProduct(odd.ProductID, prod.ID))
	if _, err := s.GetProduct(odd.ProductID); !errors.Is(err, ErrNotFound) {
		t.Errorf("merged product still there: %v", err)
	}
	if got, _ := s.GetItem(odd.ID); got.ProductID != prod.ID || got.Name != "Folding chairs" || got.Category != "Furniture" {
		t.Errorf("merged item = %+v", got)
	}
	if units, _ := s.ItemUnits(odd.ID, 2, time.Now()); units[0].ID() != "101" || units[1].ID() != "102" {
		t.Errorf("merged units = %+v", units)
	}
	if got, _ := s.GetProduct(prod.ID); got.Total != 102 || got.Notes != "Stacks 10 high" {
		t.Errorf("product after merge = %+v", got)
	}

	// Renaming an item gives it a product of its own; the last item of a
	// product going takes the product with it.
	stack := &Item{PlaceID: gym.ID, Name: "Chair cart", Quantity: 1, Portable: true}
	must(t, s.SaveItem(stack, nil, 0))
	cart := stack.ProductID
	stack.Name = "Chair dolly"
	must(t, s.SaveItem(stack, nil, 0))
	if _, err := s.GetProduct(cart); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty product kept: %v", err)
	}
	must(t, s.DeleteItem(stack.ID))
	if _, err := s.ProductByName("chair dolly"); !errors.Is(err, ErrNotFound) {
		t.Errorf("product of deleted item kept: %v", err)
	}
	// A new item can be counted from the start.
	tables := &Item{PlaceID: gym.ID, Name: "Round tables", Quantity: 12, Counted: true}
	must(t, s.SaveItem(tables, nil, 0))
	if got, _ := s.GetItem(tables.ID); !got.Counted || unitRows(t, s, tables.ID) != 0 {
		t.Errorf("tables = %+v", got)
	}
}

func TestProductsMigration(t *testing.T) {
	s, err := openAt(filepath.Join(t.TempDir(), "v12.db"), 12)
	must(t, err)
	defer s.Close()
	for _, q := range []string{
		`INSERT INTO places (id, kind, name) VALUES (1, 'site', 'Campus')`,
		`INSERT INTO places (id, parent_id, kind, name) VALUES (2, 1, 'building', 'Main'), (3, 2, 'room', 'Hall')`,
		`INSERT INTO items (id, place_id, name, category, quantity, portable) VALUES
			(1, 3, 'Lights', 'Lighting', 6, 0), (2, 2, 'lights', '', 2, 0), (3, 2, 'LIGHTS', 'Electrical', 1, 0),
			(4, 3, 'Lapel mics', 'Audio', 2, 1), (5, 2, 'Roof', '', 1, 0)`,
		`INSERT INTO item_units (item_id, number, tag, label) VALUES (1, 3, '3', 'By the door'), (4, 1, 'Mic A', '')`,
		`INSERT INTO tasks (id, item_id, name) VALUES (1, 2, 'Check')`,
	} {
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(t, s.migrate(0))

	ps, err := s.ListProducts(ProductFilter{})
	must(t, err)
	if len(ps) != 3 || ps[0].Name != "Lapel mics" || ps[1].Name != "Lights" || ps[1].Category != "Lighting" || ps[1].Total != 9 || ps[1].Counted {
		t.Fatalf("products = %+v", ps)
	}
	if it, _ := s.GetItem(2); it.Name != "Lights" || it.Category != "Lighting" || it.Quantity != 2 {
		t.Errorf("item 2 = %+v", it)
	}
	if it, _ := s.GetItem(4); !it.Portable || it.Category != "Audio" {
		t.Errorf("mics = %+v", it)
	}
	if units, _ := s.ItemUnits(1, 6, time.Now()); units[2].Label != "By the door" {
		t.Errorf("units = %+v", units)
	}
	if units, _ := s.ItemUnits(4, 2, time.Now()); units[0].ID() != "Mic A" {
		t.Errorf("mic units = %+v", units)
	}
	if ts, _ := s.ListTasks(TaskFilter{ItemID: 2}); len(ts) != 1 || ts[0].ItemName != "Lights" {
		t.Errorf("tasks = %+v", ts)
	}
	// Foreign keys are back on: deleting an item still cascades.
	must(t, s.DeleteItem(1))
	if unitRows(t, s, 1) != 0 {
		t.Error("foreign keys should be on after the migration")
	}
}

// TestFindUnit: a sticker printed for a unit finds it after it moves to
// another item of the same product.
func TestFindUnit(t *testing.T) {
	s := openTest(t)
	b := building(t, s, "Main")
	hall, gym := addPlace(t, s, b, KindRoom, "", "Hall"), addPlace(t, s, b, KindRoom, "", "Gym")
	mics := &Item{PlaceID: hall.ID, Name: "Lapel mics", Quantity: 3, Portable: true}
	must(t, s.SaveItem(mics, nil, 0))
	must(t, s.SetUnitIDs(mics.ID, 3, []string{"Mic A", "Mic B", "Mic C"}))
	if id, n, err := s.FindUnit(mics.ID, "mic b"); err != nil || id != mics.ID || n != 2 {
		t.Errorf("FindUnit = %d, %d, %v", id, n, err)
	}
	into, err := s.MoveItem(Move{ItemID: mics.ID, PlaceID: gym.ID, Units: []int{2}, MovedOn: "2026-01-01"})
	must(t, err)
	if id, n, err := s.FindUnit(mics.ID, "Mic B"); err != nil || id != into || n != 1 {
		t.Errorf("FindUnit after the move = %d, %d, %v", id, n, err)
	}
	if id, n, err := s.FindUnit(mics.ID, "Mic C"); err != nil || id != mics.ID || n != 2 {
		t.Errorf("FindUnit for one left behind = %d, %d, %v", id, n, err)
	}
	if _, _, err := s.FindUnit(mics.ID, "Mic Z"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown ID err = %v", err)
	}
	// Fixed items keep a list of their own, so only they are looked in.
	lights := &Item{PlaceID: hall.ID, Name: "Lights", Quantity: 2}
	must(t, s.SaveItem(lights, nil, 0))
	other := &Item{PlaceID: gym.ID, Name: "Lights", Quantity: 3}
	must(t, s.SaveItem(other, nil, 0))
	if _, _, err := s.FindUnit(lights.ID, "3"); !errors.Is(err, ErrNotFound) {
		t.Errorf("fixed item found another's unit: %v", err)
	}
}

func TestSupplyRequests(t *testing.T) {
	s := openTest(t)
	b := building(t, s, "Main")
	sp := &Supply{PlaceID: b.ID, Name: "Paper towels", Quantity: 20, ReorderAt: 2}
	must(t, s.SaveSupply(sp, 0))
	uid, err := s.CreateUser("pat", "Pat", "a long password", false)
	must(t, err)
	must(t, s.RequestSupply(sp.ID, uid, " The brown ones "))
	got, _ := s.GetSupply(sp.ID)
	if !got.Requested() || got.RequestedBy != "Pat" || got.RequestNote != "The brown ones" || !got.NeedsRestock() || got.Stock() != "ok" {
		t.Errorf("requested = %+v", got)
	}
	if low, _ := s.ListSupplies(SupplyFilter{LowOnly: true}); len(low) != 1 {
		t.Errorf("wanted supplies should be listed with low ones: %+v", low)
	}
	// Using some leaves the request; restocking clears it.
	must(t, s.AdjustSupply(Adjustment{SupplyID: sp.ID, Kind: "used", Amount: 1}))
	if got, _ := s.GetSupply(sp.ID); !got.Requested() {
		t.Error("using some shouldn't clear the request")
	}
	must(t, s.AdjustSupply(Adjustment{SupplyID: sp.ID, Kind: "restocked", Amount: 10}))
	if got, _ := s.GetSupply(sp.ID); got.Requested() || got.RequestNote != "" {
		t.Errorf("restocking should clear the request: %+v", got)
	}
	must(t, s.RequestSupply(sp.ID, uid, ""))
	must(t, s.CancelSupplyRequest(sp.ID))
	if got, _ := s.GetSupply(sp.ID); got.Requested() {
		t.Error("cancelled request still there")
	}
	if err := s.RequestSupply(999, uid, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestItemReportSettingMigration(t *testing.T) {
	for _, was := range []string{"0", "1"} {
		s, err := openAt(filepath.Join(t.TempDir(), "v14.db"), 14)
		must(t, err)
		must(t, s.SetSetting("public_reports", was))
		must(t, s.migrate(0))
		if got := s.Setting("public_item_reports", "?"); got != was {
			t.Errorf("public_reports %s: public_item_reports = %q", was, got)
		}
		s.Close()
	}
}

func bookable(t *testing.T, s *Store, ids ...int64) {
	t.Helper()
	must(t, s.SetBookable(ids, true))
}

func TestProductBookingOffers(t *testing.T) {
	s := openTest(t)
	b := building(t, s, "Main")
	hall, gym := addPlace(t, s, b, KindRoom, "", "Hall"), addPlace(t, s, b, KindRoom, "", "Gym")
	bookable(t, s, b.ID, hall.ID)

	tables := &Item{PlaceID: gym.ID, Name: "Tables", Quantity: 8, Portable: true}
	must(t, s.SaveItem(tables, nil, 0))
	prod, err := s.GetProduct(tables.ProductID)
	must(t, err)
	if prod.Booking != "" || len(prod.Offers) != 0 || prod.BookingLabel() != "Not offered" {
		t.Fatalf("new product = %+v", prod)
	}

	// Not bookable: refused, and nothing is written.
	prod.Booking, prod.Offers = BookingCount, []int64{hall.ID, gym.ID}
	if err := s.SaveProduct(prod); !errors.Is(err, ErrOfferNotBookable) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := s.GetProduct(prod.ID); got.Booking != "" || len(got.Offers) != 0 {
		t.Errorf("after refused save = %+v", got)
	}
	prod.Booking = "bogus"
	if err := s.SaveProduct(prod); err == nil {
		t.Error("bad booking value accepted")
	}

	// A bookable building is allowed and doesn't add its rooms.
	prod.Booking, prod.Offers = BookingCount, []int64{b.ID}
	must(t, s.SaveProduct(prod))
	got, _ := s.GetProduct(prod.ID)
	if got.Booking != BookingCount || len(got.Offers) != 1 || got.Offers[0] != b.ID || got.BookingLabel() != "Ask how many" {
		t.Fatalf("saved = %+v", got)
	}
	prod.Offers = []int64{hall.ID, b.ID}
	must(t, s.SaveProduct(prod))
	ps, err := s.ListProducts(ProductFilter{BookingOnly: true})
	must(t, err)
	if len(ps) != 1 || len(ps[0].Offers) != 2 || ps[0].Total != 8 {
		t.Fatalf("booking products = %+v", ps)
	}

	// Unbookable: drops out of Offers, kept, and back when bookable again.
	must(t, s.SetBookable([]int64{hall.ID}, false))
	if got, _ := s.GetProduct(prod.ID); len(got.Offers) != 1 || got.Offers[0] != b.ID {
		t.Errorf("offers = %v", got.Offers)
	}
	if at, _ := s.ProductsOfferedAt(hall.ID); len(at) != 1 || at[0].ID != prod.ID {
		t.Errorf("offered at hall = %+v", at)
	}
	// Saving leaves the hidden row alone.
	got, _ = s.GetProduct(prod.ID)
	got.Offers = []int64{b.ID}
	must(t, s.SaveProduct(got))
	bookable(t, s, hall.ID)
	if got, _ := s.GetProduct(prod.ID); len(got.Offers) != 2 {
		t.Errorf("offers after re-bookable = %v", got.Offers)
	}

	// Cleared booking keeps offers but leaves the booking list.
	got, _ = s.GetProduct(prod.ID)
	got.Booking = ""
	must(t, s.SaveProduct(got))
	if ps, _ := s.ListProducts(ProductFilter{BookingOnly: true}); len(ps) != 0 {
		t.Errorf("booking products = %+v", ps)
	}
	if got, _ := s.GetProduct(prod.ID); len(got.Offers) != 2 {
		t.Errorf("offers = %v", got.Offers)
	}
	if at, _ := s.ProductsOfferedAt(hall.ID); len(at) != 0 {
		t.Errorf("offered at hall = %+v", at)
	}

	// Booking set but no offers: not listed.
	got.Booking, got.Offers = BookingCount, []int64{b.ID}
	must(t, s.SaveProduct(got))
	must(t, s.SetBookable([]int64{hall.ID, b.ID}, false))
	if ps, _ := s.ListProducts(ProductFilter{BookingOnly: true}); len(ps) != 0 {
		t.Errorf("booking products with no bookable offers = %+v", ps)
	}
	bookable(t, s, hall.ID, b.ID)
	if ps, _ := s.ListProducts(ProductFilter{BookingOnly: true}); len(ps) != 1 {
		t.Errorf("booking products = %+v", ps)
	}

	// Deleting a place cascades the offer away.
	if _, err := s.DB.Exec(`DELETE FROM places WHERE id = ?`, hall.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	must(t, s.DB.QueryRow(`SELECT COUNT(*) FROM product_offers WHERE place_id = ?`, hall.ID).Scan(&n))
	if n != 0 {
		t.Error("offer survived its place")
	}
}

func TestProductBookingCheck(t *testing.T) {
	s := openTest(t)
	if _, err := s.DB.Exec(`INSERT INTO products (name, booking) VALUES ('X', 'nope')`); err == nil {
		t.Error("CHECK accepted a bad booking value")
	}
	if _, err := s.DB.Exec(`INSERT INTO products (name, booking) VALUES ('Y', 'service')`); err != nil {
		t.Error(err)
	}
}

func TestServiceProductWithNoItems(t *testing.T) {
	s := openTest(t)
	b := building(t, s, "Main")
	bookable(t, s, b.ID)
	sound := &Product{Name: "Sound", Booking: BookingService, Offers: []int64{b.ID}}
	must(t, s.CreateProduct(sound))
	if sound.ID == 0 {
		t.Fatal("no ID")
	}
	if err := s.CreateProduct(&Product{Name: "sound"}); !errors.Is(err, ErrProductExists) {
		t.Errorf("duplicate err = %v", err)
	}
	if err := s.CreateProduct(&Product{Name: "Bad", Offers: []int64{9999}}); !errors.Is(err, ErrOfferNotBookable) {
		t.Errorf("bad offer err = %v", err)
	}
	if got, err := s.ProductByName("Sound"); err != nil || got.BookingLabel() != "Ask yes or no" || got.Total != 0 {
		t.Errorf("sound = %+v, %v", got, err)
	}

	// Item edits and deletes don't sweep it away; plain empty products go.
	it := &Item{PlaceID: b.ID, Name: "Chairs", Quantity: 2}
	must(t, s.SaveItem(it, nil, 0))
	it.Name = "Stools"
	must(t, s.SaveItem(it, nil, 0))
	must(t, s.DeleteItem(it.ID))
	if _, err := s.GetProduct(sound.ID); err != nil {
		t.Errorf("service product lost: %v", err)
	}
	if _, err := s.ProductByName("Chairs"); err == nil {
		t.Error("empty plain product should be dropped")
	}
}

func TestNonPortableItemsAndTotals(t *testing.T) {
	s := openTest(t)
	b := building(t, s, "Main")
	hall := addPlace(t, s, b, KindRoom, "", "Hall")
	must(t, s.SaveItem(&Item{PlaceID: hall.ID, Name: "Tables", Quantity: 4, Portable: true}, nil, 0))
	fixed := &Item{PlaceID: b.ID, Name: "Tables", Quantity: 3}
	must(t, s.SaveItem(fixed, nil, 0))
	must(t, s.SaveItem(&Item{PlaceID: hall.ID, Name: "Cots", Category: "Beds", Counted: true, Quantity: 5}, nil, 0))
	must(t, s.SaveItem(&Item{PlaceID: hall.ID, Name: "Cots", Quantity: 6}, nil, 0))

	tables, _ := s.ProductByName("Tables")
	np, err := s.NonPortableItems(tables.ID)
	must(t, err)
	if len(np) != 1 || np[0].ID != fixed.ID {
		t.Errorf("non-portable = %+v", np)
	}
	if tables.Total != 7 {
		t.Errorf("tracked total = %d", tables.Total)
	}
	if cots, _ := s.ProductByName("Cots"); !cots.Counted || cots.Total != 11 {
		t.Errorf("counted total = %+v", cots)
	}
}

func TestMergeProductCarriesOffers(t *testing.T) {
	s := openTest(t)
	b := building(t, s, "Main")
	bookable(t, s, b.ID)
	a := &Product{Name: "A"}
	must(t, s.CreateProduct(a))
	c := &Product{Name: "C", Booking: BookingService, Offers: []int64{b.ID}}
	must(t, s.CreateProduct(c))
	must(t, s.MergeProduct(c.ID, a.ID))
	got, err := s.GetProduct(a.ID)
	must(t, err)
	if got.Booking != BookingService || len(got.Offers) != 1 {
		t.Errorf("merged = %+v", got)
	}
}
