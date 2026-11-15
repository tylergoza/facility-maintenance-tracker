package server

import (
	"net/url"
	"strings"
	"testing"
)

// Correcting a history entry puts back what it took from stock and takes
// the new amount, never twice; the task's dates follow the latest entry.
func TestLogEdit(t *testing.T) {
	st, _, ed := meFixture(t)
	meMaintFixture(t, ed) // supply 3 Furnace filters (5); task 1 done Jan 10 and Apr 12, 1 taken each: 3 left
	stock := func() int {
		sp, err := st.GetSupply(3)
		if err != nil {
			t.Fatal(err)
		}
		return sp.Quantity
	}
	if got := stock(); got != 3 {
		t.Fatalf("stock before = %d", got)
	}

	page := ed.get("/logs/2/edit", 200)
	for _, want := range []string{"Took it from supplies", `value="2026-04-12"`, `name="took_supply" value="1" checked`} {
		if !strings.Contains(page, want) {
			t.Errorf("edit page lacks %q", want)
		}
	}

	// The Apr 12 entry really took 3 and was on the 15th, with a note.
	ed.post("/logs/2/edit", "/logs/2/edit", url.Values{"performed_on": {"2026-04-15"}, "performed_by": {"Eddie"},
		"took_supply": {"1"}, "supply_amount": {"3"}, "notes": {"Filters were very dirty"}}, 200)
	if got := stock(); got != 1 { // 3 + 1 back - 3 taken
		t.Errorf("stock after first correction = %d, want 1", got)
	}
	l, err := st.GetLog(2)
	if err != nil {
		t.Fatal(err)
	}
	if l.PerformedOn != "2026-04-15" || l.Notes != "Filters were very dirty" || l.SupplyUsed != "3 × Furnace filters" {
		t.Errorf("log = %+v", l)
	}
	task, _ := st.GetTask(1)
	if task.LastCompletedOn != "2026-04-15" || task.NextDueOn != "2026-07-15" {
		t.Errorf("task dates = %s / %s", task.LastCompletedOn, task.NextDueOn)
	}

	// Then: none came from stock after all. The 3 go back once.
	ed.post("/logs/2/edit", "/logs/2/edit", url.Values{"performed_on": {"2026-04-15"}, "notes": {"Filters looked fine"}}, 200)
	if got := stock(); got != 4 {
		t.Errorf("stock after second correction = %d, want 4", got)
	}
	if l, _ := st.GetLog(2); l.SupplyUsed != "" || l.Replaced != 0 {
		t.Errorf("log still shows stock taken: %+v", l)
	}

	// Moving the older entry's date doesn't move the task back.
	ed.post("/logs/1/edit", "/logs/1/edit", url.Values{"performed_on": {"2026-01-11"}, "took_supply": {"1"}, "supply_amount": {"1"}}, 200)
	if task, _ := st.GetTask(1); task.LastCompletedOn != "2026-04-15" {
		t.Errorf("older correction moved last completed to %s", task.LastCompletedOn)
	}
	if got := stock(); got != 4 { // 1 back, 1 taken
		t.Errorf("stock after older correction = %d, want 4", got)
	}

	// Bad input is shown again, not saved.
	ed.post("/logs/2/edit", "/logs/2/edit", url.Values{"performed_on": {"2999-01-01"}}, 422)
}
