package main

import (
	"time"

	"github.com/mountainview/maintenance-tracker/internal/store"
)

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
	type room struct{ number, name string }
	type building struct {
		name, address string
		rooms         []room
		items         []item
	}

	data := []building{
		{"Sanctuary", "100 Church St", []room{{"100", "Worship Hall"}, {"104", "Nursery"}, {"B1", "Boiler Room"}, {"201", "Sound Booth"}}, []item{
			{"Furnace (north)", "HVAC", "Boiler Room", []task{{"Replace air filter", "months", 3, -12, 102}, {"Annual service & inspection", "years", 1, 45, 320}}},
			{"Water heater", "Plumbing", "Boiler Room", []task{{"Flush tank", "years", 1, 9, 356}}},
			{"Smoke detectors", "Safety", "Nursery", []task{{"Test alarms", "months", 1, 3, 27}, {"Replace batteries", "years", 1, 160, 205}}},
			{"Mixing console", "Audio/Visual", "Sound Booth", []task{{"Firmware check & clean faders", "months", 6, 70, 110}}},
			{"Grand piano", "Instruments", "Worship Hall", []task{{"Tune piano", "months", 6, 21, 160}}},
			{"Roof", "Roof", "", []task{{"Inspect roof & clear gutters", "months", 6, -3, 186}}},
			{"Fire extinguishers", "Safety", "", []task{{"Annual certification", "years", 1, 120, 245}}},
		}},
		{"Fellowship Hall", "100 Church St (rear)", []room{{"110", "Kitchen"}, {"112", "Classroom 1"}, {"114", "Classroom 2"}}, []item{
			{"Commercial range hood", "Kitchen", "Kitchen", []task{{"Clean grease filters", "months", 3, 25, 65}, {"Fire suppression inspection", "months", 6, -30, 210}}},
			{"Refrigerator", "Kitchen", "Kitchen", []task{{"Clean condenser coils", "months", 6, 100, 80}}},
			{"Mini-split AC", "HVAC", "Classroom 1", []task{{"Clean filters", "months", 1, 12, 18}}},
			{"Parking lot", "Grounds", "", []task{{"Re-stripe lines", "years", 3, 400, 700}}},
		}},
		{"Parsonage", "12 Elm St", []room{{"", "Basement"}, {"", "Kitchen"}}, []item{
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

	// Tasks that use a supply each time: task name -> supply name.
	taskSupplies := map[string]string{
		"Replace air filter": "Furnace filters 20x25x1",
		"Replace batteries":  "AA batteries",
	}

	for _, bd := range data {
		b := &store.Building{Name: bd.name, Address: bd.address}
		if err := st.SaveBuilding(b); err != nil {
			return err
		}
		rooms := map[string]int64{}
		for _, rn := range bd.rooms {
			r := &store.Room{BuildingID: b.ID, Number: rn.number, Name: rn.name}
			if err := st.SaveRoom(r); err != nil {
				return err
			}
			rooms[rn.name] = r.ID
		}
		supplyIDs := map[string]int64{}
		for _, sp := range supplies[bd.name] {
			supply := &store.Supply{BuildingID: b.ID, RoomID: rooms[sp.room], Name: sp.name, Unit: sp.unit, Quantity: sp.quantity, ReorderAt: sp.low}
			if sp.name == "Mop heads" {
				supply.Reusable, supply.InUse, supply.Cleaning = true, 2, 1
			}
			if err := st.SaveSupply(supply, 0); err != nil {
				return err
			}
			supplyIDs[sp.name] = supply.ID
		}
		for _, it := range bd.items {
			i := &store.Item{BuildingID: b.ID, RoomID: rooms[it.room], Name: it.name, Category: it.category}
			if err := st.SaveItem(i); err != nil {
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
	}
	return nil
}
