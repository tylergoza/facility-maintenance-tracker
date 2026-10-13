package server

import (
	"net/url"
	"strings"
	"testing"
)

func TestPlaceBookable(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"site_name": {"Grace Church"}, "username": {"admin"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	// Site 1, Main 2, 2nd floor 3, Nursery 4, Library 5.
	addPlaces(c, "building:Main", "floor:2::2nd floor", "room:3::Nursery", "room:3::Library")

	// A room saved with the box ticked is bookable; a floor never is.
	c.post("/places/4/edit", "/places/4", url.Values{"kind": {"room"}, "parent_id": {"3"}, "name": {"Nursery"}, "bookable": {"1"}}, 200)
	c.post("/places/3/edit", "/places/3", url.Values{"kind": {"floor"}, "parent_id": {"2"}, "name": {"2nd floor"}, "bookable": {"1"}}, 200)
	if p, _ := st.GetPlace(4); !p.Bookable {
		t.Error("room should be bookable")
	}
	if p, _ := st.GetPlace(3); p.Bookable {
		t.Error("floor should not be bookable")
	}

	// ?bookable=1 lists only the bookable ones.
	all := c.get("/places", 200)
	only := c.get("/places?bookable=1", 200)
	if !strings.Contains(all, "Library") || !strings.Contains(only, "Nursery") || strings.Contains(only, "Library") {
		t.Error("bookable filter should list only the Nursery")
	}

	// Bulk mark on and off for ticked places.
	page := c.get("/places/bookable?parent=2", 200)
	if !strings.Contains(page, `value="4" checked`) || strings.Contains(page, `value="5" checked`) {
		t.Errorf("checkboxes wrong:\n%s", page)
	}
	c.post("/places/bookable", "/places/bookable", url.Values{"id": {"3", "5"}, "mark": {"on"}}, 200)
	if p, _ := st.GetPlace(5); !p.Bookable {
		t.Error("Library should be bookable")
	}
	if p, _ := st.GetPlace(3); p.Bookable {
		t.Error("floor should stay not bookable")
	}
	c.post("/places/bookable", "/places/bookable", url.Values{"id": {"4", "5"}, "mark": {"off"}}, 200)
	for _, id := range []int64{4, 5} {
		if p, _ := st.GetPlace(id); p.Bookable {
			t.Errorf("place %d should not be bookable", id)
		}
	}

	// Bulk add with the box ticked.
	c.post("/places/bulk", "/places/bulk", url.Values{"parent_id": {"3"}, "kind": {"room"}, "lines": {"Hall\nKitchen"}, "bookable": {"1"}}, 200)
	if p, _ := st.GetPlace(6); !p.Bookable {
		t.Error("bulk-added room should be bookable")
	}
}
