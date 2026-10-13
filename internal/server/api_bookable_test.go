package server

import (
	"net/url"
	"testing"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

func TestAPIPlacesBookable(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"username": {"admin"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	body := c.post("/admin/tokens", "/admin/tokens", url.Values{"name": {"Church calendar"}}, 200)
	m := tokenRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("the new token should be shown once")
	}
	token := m[1]

	site := &store.Place{Kind: "site", Name: "Main"}
	if err := st.SavePlace(site); err != nil {
		t.Fatal(err)
	}
	building := &store.Place{Kind: "building", ParentID: site.ID, Name: "Hall", Bookable: true}
	if err := st.SavePlace(building); err != nil {
		t.Fatal(err)
	}
	bookable := &store.Place{Kind: "room", ParentID: building.ID, Number: "104", Name: "Nursery", Bookable: true, Tags: []string{"classrooms"}}
	if err := st.SavePlace(bookable); err != nil {
		t.Fatal(err)
	}
	other := &store.Place{Kind: "room", ParentID: building.ID, Number: "105", Name: "Storage", Tags: []string{"classrooms"}}
	if err := st.SavePlace(other); err != nil {
		t.Fatal(err)
	}

	ids := func(places []apiPlaceJSON) map[int64]bool {
		out := map[int64]bool{}
		for _, p := range places {
			out[p.ID] = true
		}
		return out
	}

	var all struct{ Places []apiPlaceJSON }
	c.apiGet(token, "/api/v1/places", 200, &all)
	if len(all.Places) != 4 {
		t.Fatalf("places = %+v", all.Places)
	}
	want := map[int64]bool{site.ID: false, building.ID: true, bookable.ID: true, other.ID: false}
	for _, p := range all.Places {
		if p.Bookable != want[p.ID] {
			t.Errorf("place %d (%s) bookable = %v, want %v", p.ID, p.Name, p.Bookable, want[p.ID])
		}
	}

	var got struct{ Places []apiPlaceJSON }
	c.apiGet(token, "/api/v1/places?bookable=1", 200, &got)
	if len(got.Places) != 2 || !ids(got.Places)[building.ID] || !ids(got.Places)[bookable.ID] {
		t.Errorf("bookable places = %+v", got.Places)
	}

	c.apiGet(token, "/api/v1/places?bookable=1&tag=classrooms", 200, &got)
	if len(got.Places) != 1 || got.Places[0].ID != bookable.ID {
		t.Errorf("bookable classrooms = %+v", got.Places)
	}
}
