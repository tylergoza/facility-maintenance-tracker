package main

import (
	"errors"
	"time"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

var errSeedSites = errors.New("there are several sites; seed-demo only fills an empty database")

// seedDemo fills an empty database with realistic sample data so the
// dashboard has something to show. Dates are relative to today.
func seedDemo(st *store.Store) error {
	day := func(offset int) string { return time.Now().AddDate(0, 0, offset).Format(store.DateLayout) }

	type task struct {
		name, unit string
		every, due int // due: days from today (negative = overdue)
		last       int // days ago last done (0 = never)
	}
	type item struct {
		name, category, room string
		tasks                []task
	}
	// room places go inside the place named in "in" ("" = the building).
	type room struct {
		kind, number, name, in string
		tags                   []string
	}
	type building struct {
		name, address string
		rooms         []room
		items         []item
	}
	floor := func(name string) room { return room{kind: store.KindFloor, name: name} }
	in := func(where, number, name string, tags ...string) room {
		return room{kind: store.KindRoom, number: number, name: name, in: where, tags: tags}
	}

	data := []building{
		{"Sanctuary", "100 Church St", []room{
			floor("Basement"), floor("1st floor"), floor("Balcony"),
			in("1st floor", "100", "Worship Hall"), in("1st floor", "104", "Nursery", "kids"), in("Basement", "B1", "Boiler Room"),
			in("Balcony", "201", "Sound Booth", "media"), {kind: store.KindZone, name: "Front porch"},
		}, []item{
			{"Furnace (north)", "HVAC", "Boiler Room", []task{{"Replace air filter", "months", 3, -12, 102}, {"Annual service & inspection", "years", 1, 45, 320}}},
			{"Water heater", "Plumbing", "Boiler Room", []task{{"Flush tank", "years", 1, 9, 356}}},
			{"Smoke detectors", "Safety", "Nursery", []task{{"Test alarms", "months", 1, 3, 27}, {"Replace batteries", "years", 1, 160, 205}}},
			{"Mixing console", "Audio/Visual", "Sound Booth", []task{{"Firmware check & clean faders", "months", 6, 70, 110}}},
			{"Grand piano", "Instruments", "Worship Hall", []task{{"Tune piano", "months", 6, 21, 160}}},
			{"Roof", "Roof", "", []task{{"Inspect roof & clear gutters", "months", 6, -3, 186}}},
			{"Fire extinguishers", "Safety", "", []task{{"Annual certification", "years", 1, 120, 245}}},
		}},
		{"Fellowship Hall", "100 Church St (rear)", []room{
			in("", "110", "Kitchen"), {kind: store.KindZone, name: "Classrooms", tags: []string{"kids"}},
			in("Classrooms", "112", "Classroom 1"), in("Classrooms", "114", "Classroom 2"),
		}, []item{
			{"Commercial range hood", "Kitchen", "Kitchen", []task{{"Clean grease filters", "months", 3, 25, 65}, {"Fire suppression inspection", "months", 6, -30, 210}}},
			{"Refrigerator", "Kitchen", "Kitchen", []task{{"Clean condenser coils", "months", 6, 100, 80}}},
			{"Mini-split AC", "HVAC", "Classroom 1", []task{{"Clean filters", "months", 1, 12, 18}}},
		}},
		{"Parsonage", "12 Elm St", []room{in("", "", "Basement"), in("", "", "Kitchen")}, []item{
			{"Sump pump", "Plumbing", "Basement", []task{{"Test pump", "months", 3, 40, 50}}},
			{"Dryer vent", "Safety", "Basement", []task{{"Clean dryer vent", "years", 1, 0, 0}}},
		}},
	}

	type supply struct {
		name, unit, room string
		quantity, low    int
	}
	supplies := map[string][]supply{
		"Sanctuary": {
			{"Furnace filters 20x25x1", "filters", "Boiler Room", 1, 2},
			{"Diapers (size 3)", "packs", "Nursery", 4, 2},
			{"Disinfecting wipes", "tubs", "Nursery", 0, 1},
			{"AA batteries", "", "Sound Booth", 24, 8},
			{"Light bulbs (LED A19)", "bulbs", "", 18, 6},
			{"Mop heads", "", "", 3, 1}, // reusable: see below
		},
		"Fellowship Hall": {
			{"Paper towels", "rolls", "Kitchen", 9, 6},
			{"Coffee filters", "packs", "Kitchen", 3, 2},
			{"Dry-erase markers", "", "Classroom 1", 12, 4},
		},
	}

	// Groups of identical things, some using a supply, plus portable gear.
	type counted struct {
		name, category, room string
		quantity             int
		supply               string
		per                  int
		portable             bool
	}
	countedItems := map[string][]counted{
		"Sanctuary": {
			{"Ceiling pendants", "Lighting", "Worship Hall", 8, "Light bulbs (LED A19)", 1, false},
			{"Electrical outlets", "Electrical", "Worship Hall", 12, "", 0, false},
			{"Exit signs", "Safety", "Worship Hall", 2, "", 0, false},
			{"Porch lights", "Lighting", "Front porch", 4, "Light bulbs (LED A19)", 1, false},
			{"Ceiling cans", "Lighting", "Nursery", 6, "LED BR30 bulbs", 1, false},
			{"Electrical outlets", "Electrical", "Nursery", 6, "", 0, false},
			{"Light switches", "Electrical", "Nursery", 2, "", 0, false},
			{"Projector", "Audio/Visual", "Sound Booth", 1, "", 0, true},
		},
		"Fellowship Hall": {
			{"Troffers", "Lighting", "Classroom 1", 4, "LED T8 tubes", 2, false},
			{"Air returns", "HVAC", "Classroom 1", 2, "Air filters 12x20x1", 1, false},
			{"Portable TV cart", "Audio/Visual", "Classroom 2", 1, "", 0, true},
		},
	}
	// Supplies the items above use that aren't in the supplies list yet.
	newSupplies := map[string]store.Supply{
		"LED BR30 bulbs":      {Unit: "bulbs", Quantity: 2, ReorderAt: 1},
		"LED T8 tubes":        {Unit: "tubes", Quantity: 0, ReorderAt: 2},
		"Air filters 12x20x1": {Unit: "filters", Quantity: 4, ReorderAt: 2},
	}

	type problem struct{ title, details, room, reporter string }
	problems := map[string][]problem{
		"Sanctuary":       {{"Nursery door won't latch", "Have to pull it hard to close.", "Nursery", "Front desk"}},
		"Fellowship Hall": {{"Kitchen faucet dripping", "Hot side, constant drip.", "Kitchen", "Kitchen volunteers"}},
	}

	// Tasks that use a supply each time: task name -> supply name.
	taskSupplies := map[string]string{
		"Replace air filter": "Furnace filters 20x25x1",
		"Replace batteries":  "AA batteries",
	}

	site, err := st.EnsureSite(st.Setting("site_name", "Facility Maintenance"))
	if err != nil {
		return err
	}
	if site == 0 {
		return errSeedSites
	}
	// The parking lot is the whole site's, not any one building's.
	lot := &store.Item{PlaceID: site, Name: "Parking lot", Category: "Grounds"}
	if err := st.SaveItem(lot, nil, 0); err != nil {
		return err
	}
	if err := st.SaveTask(&store.Task{ItemID: lot.ID, Name: "Re-stripe lines", IntervalValue: 3, IntervalUnit: "years", Active: true,
		NextDueOn: day(400), LastCompletedOn: day(-700), SupplyAmount: 1, SupplyAlways: true}); err != nil {
		return err
	}

	for _, bd := range data {
		b := &store.Place{ParentID: site, Kind: store.KindBuilding, Name: bd.name, Address: bd.address}
		if err := st.SavePlace(b); err != nil {
			return err
		}
		rooms := map[string]int64{"": b.ID}
		for _, rn := range bd.rooms {
			r := &store.Place{ParentID: rooms[rn.in], Kind: rn.kind, Number: rn.number, Name: rn.name, Tags: rn.tags}
			if err := st.SavePlace(r); err != nil {
				return err
			}
			rooms[rn.name] = r.ID
		}
		supplyIDs := map[string]int64{}
		for _, sp := range supplies[bd.name] {
			supply := &store.Supply{PlaceID: rooms[sp.room], Name: sp.name, Unit: sp.unit, Quantity: sp.quantity, ReorderAt: sp.low}
			if sp.name == "Mop heads" {
				supply.Reusable, supply.InUse, supply.Cleaning = true, 2, 1
			}
			if err := st.SaveSupply(supply, 0); err != nil {
				return err
			}
			supplyIDs[sp.name] = supply.ID
		}
		for _, pr := range problems[bd.name] {
			p := &store.Problem{PlaceID: rooms[pr.room], Title: pr.title, Details: pr.details, ReporterName: pr.reporter}
			if err := st.CreateProblem(p); err != nil {
				return err
			}
		}
		for _, it := range bd.items {
			i := &store.Item{PlaceID: rooms[it.room], Name: it.name, Category: it.category}
			if err := st.SaveItem(i, nil, 0); err != nil {
				return err
			}
			for _, tk := range it.tasks {
				t := &store.Task{ItemID: i.ID, Name: tk.name, IntervalValue: tk.every, IntervalUnit: tk.unit, Active: true,
					SupplyID: supplyIDs[taskSupplies[tk.name]], SupplyAmount: 1, SupplyAlways: true}
				if tk.due != 0 {
					t.NextDueOn = day(tk.due)
				}
				if tk.last != 0 {
					t.LastCompletedOn = day(-tk.last)
				}
				if err := st.SaveTask(t); err != nil {
					return err
				}
				if tk.last != 0 {
					if _, err := st.DB.Exec(`INSERT INTO maintenance_logs (item_id, task_id, performed_on, performed_by, notes) VALUES (?, ?, ?, ?, ?)`,
						i.ID, t.ID, t.LastCompletedOn, "Facilities team", "Routine "+tk.name+"."); err != nil {
						return err
					}
				}
			}
		}
		for _, c := range countedItems[bd.name] {
			i := &store.Item{PlaceID: rooms[c.room], Name: c.name, Category: c.category, Quantity: c.quantity,
				SupplyID: supplyIDs[c.supply], SupplyPer: c.per, Portable: c.portable}
			var sp *store.Supply
			if def, ok := newSupplies[c.supply]; ok && i.SupplyID == 0 {
				def.Name = c.supply
				sp = &def
			}
			if err := st.SaveItem(i, sp, 0); err != nil {
				return err
			}
			if sp != nil {
				supplyIDs[c.supply] = sp.ID
			}
		}
	}
	return nil
}
