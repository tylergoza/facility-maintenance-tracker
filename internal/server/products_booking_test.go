package server

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// bookingFixture sets up a site (1), a bookable building Hall (2), a
// bookable room Nursery (3), a room Storage (4) that isn't bookable, and
// an API token.
func bookingFixture(t *testing.T) (*client, *store.Store, string) {
	t.Helper()
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"username": {"admin"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	body := c.post("/admin/tokens", "/admin/tokens", url.Values{"name": {"Church calendar"}}, 200)
	m := tokenRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("the new token should be shown once")
	}
	site := &store.Place{Kind: "site", Name: "Main"}
	hall := &store.Place{Kind: "building", Name: "Hall", Bookable: true}
	for _, p := range []*store.Place{site, hall} {
		if p == hall {
			p.ParentID = site.ID
		}
		if err := st.SavePlace(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []*store.Place{
		{Kind: "room", ParentID: hall.ID, Name: "Nursery", Bookable: true},
		{Kind: "room", ParentID: hall.ID, Name: "Storage"},
	} {
		if err := st.SavePlace(p); err != nil {
			t.Fatal(err)
		}
	}
	return c, st, m[1]
}

func TestProductBookingForm(t *testing.T) {
	c, st, _ := bookingFixture(t)
	chairs := &store.Item{PlaceID: 4, Name: "Folding chairs", Quantity: 10, Portable: true, Counted: true}
	if err := st.SaveItem(chairs, nil, 0); err != nil {
		t.Fatal(err)
	}
	pid := chairs.ProductID

	form := c.get("/products/1/edit", 200)
	for _, want := range []string{"On the church calendar", "Hall (the whole building)", "Nursery"} {
		if !strings.Contains(form, want) {
			t.Errorf("form is missing %q", want)
		}
	}
	if strings.Contains(form, "Storage") {
		t.Error("a room that isn't bookable shouldn't be listed")
	}

	// The building on its own doesn't tick its rooms.
	c.post("/products/1/edit", "/products/1", url.Values{"name": {"Folding chairs"}, "tracking": {"count"},
		"booking": {"count"}, "offer": {"2"}}, 200)
	p, _ := st.GetProduct(pid)
	if p.Booking != store.BookingCount || len(p.Offers) != 1 || p.Offers[0] != 2 {
		t.Errorf("product = %+v", p)
	}

	// Turning it off keeps the offers for later.
	c.post("/products/1/edit", "/products/1", url.Values{"name": {"Folding chairs"}, "tracking": {"count"}}, 200)
	p, _ = st.GetProduct(pid)
	if p.Booking != "" || len(p.Offers) != 1 {
		t.Errorf("after turning off: %+v", p)
	}

	// A place that isn't bookable is refused.
	body := c.post("/products/1/edit", "/products/1", url.Values{"name": {"Folding chairs"}, "tracking": {"count"},
		"booking": {"count"}, "offer": {"4"}}, 422)
	if !strings.Contains(body, "booked any more") {
		t.Errorf("want a form error:\n%s", body)
	}
}

func TestProductBookingPortableWarning(t *testing.T) {
	c, st, _ := bookingFixture(t)
	fixed := &store.Item{PlaceID: 4, Name: "Pews", Quantity: 4, Counted: true}
	if err := st.SaveItem(fixed, nil, 0); err != nil {
		t.Fatal(err)
	}
	c.post("/products/1/edit", "/products/1", url.Values{"name": {"Pews"}, "tracking": {"count"}, "booking": {"count"}, "offer": {"3"}}, 200)
	page := c.get("/products/1", 200)
	if !strings.Contains(page, "make them portable") || !strings.Contains(page, "/items/1/edit") {
		t.Errorf("want the portable warning:\n%s", page)
	}
	// A service doesn't need portable items.
	c.post("/products/1/edit", "/products/1", url.Values{"name": {"Pews"}, "tracking": {"count"}, "booking": {"service"}, "offer": {"3"}}, 200)
	if page := c.get("/products/1", 200); strings.Contains(page, "make them portable") {
		t.Error("no warning for a service")
	}
}

func TestProductNewWithoutItems(t *testing.T) {
	c, st, token := bookingFixture(t)
	c.get("/products/new", 200)
	c.post("/products/new", "/products", url.Values{"name": {"Sound"}, "tracking": {"each"}, "booking": {"service"}, "offer": {"2"}}, 200)
	p, err := st.ProductByName("Sound")
	if err != nil || p.Booking != store.BookingService {
		t.Fatalf("Sound = %+v, %v", p, err)
	}
	if page := c.get("/products/1", 200); !strings.Contains(page, "A service: no items") {
		t.Errorf("service page:\n%s", page)
	}
	// Names are unique, and bad offers are refused.
	c.post("/products/new", "/products", url.Values{"name": {"Sound"}}, 422)
	c.post("/products/new", "/products", url.Values{"name": {"Lights"}, "booking": {"service"}, "offer": {"4"}}, 422)

	// With no offer at all it's made but not listed for the calendar.
	c.post("/products/new", "/products", url.Values{"name": {"Mics"}, "booking": {"service"}}, 200)
	if page := c.get("/products/2", 200); !strings.Contains(page, "A service: no items") {
		t.Error("Mics page should render")
	}

	list := c.get("/products", 200)
	if !strings.Contains(list, "Calendar") || !strings.Contains(list, "New product") {
		t.Errorf("products list:\n%s", list)
	}
	only := c.get("/products?booking=1", 200)
	if !strings.Contains(only, "Sound") || strings.Contains(only, "Mics") {
		t.Errorf("?booking=1 should list Sound only:\n%s", only)
	}
	if none := c.get("/products?booking=1&q=zzz", 200); !strings.Contains(none, "No products match") {
		t.Error("filter with no matches")
	}

	var got struct{ Products []apiProductJSON }
	c.apiGet(token, "/api/v1/products", 200, &got)
	if len(got.Products) != 2 || got.Products[1].Booking != "service" || len(got.Products[1].OfferedIn) != 1 || len(got.Products[0].OfferedIn) != 0 {
		t.Errorf("products = %+v", got.Products)
	}
}

func TestAPIProductBooking(t *testing.T) {
	c, st, token := bookingFixture(t)
	chairs := &store.Item{PlaceID: 4, Name: "Chairs", Quantity: 10, Portable: true, Counted: true}
	if err := st.SaveItem(chairs, nil, 0); err != nil {
		t.Fatal(err)
	}
	tables := &store.Item{PlaceID: 4, Name: "Tables", Quantity: 5, Portable: true, Counted: true}
	if err := st.SaveItem(tables, nil, 0); err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetProduct(chairs.ProductID)
	p.Booking, p.Offers = store.BookingCount, []int64{2, 3}
	if err := st.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	// Tables are set to be asked for but offered nowhere.
	q, _ := st.GetProduct(tables.ProductID)
	q.Booking = store.BookingCount
	if err := st.SaveProduct(q); err != nil {
		t.Fatal(err)
	}

	var got struct{ Products []apiProductJSON }
	c.apiGet(token, "/api/v1/products?booking=1", 200, &got)
	if len(got.Products) != 1 || got.Products[0].ID != p.ID || len(got.Products[0].OfferedIn) != 2 {
		t.Errorf("?booking=1 = %+v", got.Products)
	}
	c.apiGet(token, "/api/v1/products?booking=1&q=Tables", 200, &got)
	if len(got.Products) != 0 {
		t.Errorf("no offers should not be listed: %+v", got.Products)
	}

	// Turning it off: stored offers are not sent, and it's an array.
	p.Booking = ""
	if err := st.SaveProduct(p); err != nil {
		t.Fatal(err)
	}
	var one apiProductJSON
	c.apiGet(token, "/api/v1/products/"+strconv.FormatInt(p.ID, 10), 200, &one)
	if one.Booking != "" || one.OfferedIn == nil || len(one.OfferedIn) != 0 {
		t.Errorf("unoffered = %+v", one)
	}
}

func TestPlaceNotBookableDropsOffers(t *testing.T) {
	c, st, token := bookingFixture(t)
	p := &store.Product{Name: "Sound", Booking: store.BookingService, Offers: []int64{2, 3}}
	if err := st.CreateProduct(p); err != nil {
		t.Fatal(err)
	}
	if page := c.get("/places/3", 200); !strings.Contains(page, "Offered here on the calendar: Sound.") {
		t.Errorf("delete confirm should name Sound:\n%s", page)
	}
	if page := c.get("/places/4", 200); strings.Contains(page, "Offered here on the calendar") {
		t.Error("Storage has no offers")
	}

	// Saved as not bookable: it says what's no longer offered there.
	body := c.post("/places/3/edit", "/places/3", url.Values{"kind": {"room"}, "parent_id": {"2"}, "name": {"Nursery"}}, 200)
	if !strings.Contains(body, "No longer offered here: Sound.") {
		t.Errorf("want the note:\n%s", body)
	}
	var one apiProductJSON
	c.apiGet(token, "/api/v1/products/1", 200, &one)
	if len(one.OfferedIn) != 1 || one.OfferedIn[0] != 2 {
		t.Errorf("offered_in = %v", one.OfferedIn)
	}

	// The same for the bulk page.
	body = c.post("/places/bookable", "/places/bookable", url.Values{"id": {"2"}, "mark": {"off"}}, 200)
	if !strings.Contains(body, "No longer offered here: Sound.") {
		t.Errorf("bulk: want the note:\n%s", body)
	}
	c.apiGet(token, "/api/v1/products/1", 200, &one)
	if len(one.OfferedIn) != 0 {
		t.Errorf("offered_in = %v", one.OfferedIn)
	}
}
