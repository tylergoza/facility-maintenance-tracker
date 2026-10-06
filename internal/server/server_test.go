package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newTestServer(t *testing.T) (*client, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(Config{}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}, st
}

func (c *client) get(path string, wantStatus int) string {
	c.t.Helper()
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("GET %s: status %d, want %d\n%s", path, resp.StatusCode, wantStatus, body)
	}
	return string(body)
}

var csrfRe = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

// post submits a form, pulling the CSRF token from a page first.
func (c *client) post(tokenPage, path string, form url.Values, wantStatus int) string {
	c.t.Helper()
	m := csrfRe.FindStringSubmatch(c.get(tokenPage, 200))
	if m == nil {
		c.t.Fatalf("no csrf token on %s", tokenPage)
	}
	form.Set("_csrf", m[1])
	resp, err := c.http.PostForm(c.base+path, form)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("POST %s: status %d, want %d\n%s", path, resp.StatusCode, wantStatus, body)
	}
	return string(body)
}

func TestEndToEnd(t *testing.T) {
	c, st := newTestServer(t)

	// No users yet: dashboard redirects to setup.
	if body := c.get("/", 200); !strings.Contains(body, "Create the first administrator") {
		t.Fatal("expected setup page")
	}
	c.post("/setup", "/setup", url.Values{
		"site_name": {"Grace Church"}, "username": {"pastor"}, "display_name": {"Pastor Sam"},
		"password": {"a long password"}, "password_confirm": {"a long password"},
	}, 200)

	// The first building goes on a site made for it (place 1): the
	// building is place 2 and its first room place 3.
	c.post("/places/new", "/places", url.Values{"kind": {"building"}, "name": {"Sanctuary"}, "address": {"1 Main St"}}, 200)
	c.post("/places/new?parent=2", "/places", url.Values{"kind": {"room"}, "parent_id": {"2"}, "name": {"Nursery"}}, 200)
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Smoke detector"}, "category": {"Safety"}}, 200)
	c.post("/items/new", "/items", url.Values{"place_id": {"2"}, "name": {"Roof"}}, 200)
	c.post("/tasks/new?item=1", "/tasks", url.Values{
		"item_id": {"1"}, "name": {"Test alarm"}, "recurring": {"1"}, "interval_value": {"6"}, "interval_unit": {"months"},
		"last_completed_on": {"2020-01-01"},
	}, 200)
	c.post("/tasks/new?item=2", "/tasks", url.Values{"item_id": {"2"}, "name": {"Inspect shingles"}}, 200)

	task, err := st.GetTask(1)
	if err != nil || task.NextDueOn != "2020-07-01" {
		t.Fatalf("next due should be computed from last done: %+v %v", task, err)
	}

	// Items need a place that exists.
	if body := c.post("/items/new", "/items", url.Values{"place_id": {"99"}, "name": {"X"}}, 422); !strings.Contains(body, "Choose where it is") {
		t.Fatal("expected place validation error")
	}

	// Every signed-in page renders.
	for _, p := range []string{
		"/", "/?place=2", "/places", "/places/1", "/places/2", "/places/2/edit", "/places/new", "/places/new?kind=site", "/places/bulk?parent=2",
		"/places/3", "/places/3/edit", "/places/2?all=1",
		"/items", "/items?q=smoke&place=2", "/items/new", "/items/1", "/items/1/edit", "/items/1/log",
		"/tasks/new?item=1", "/tasks/1/edit", "/tasks/1/complete", "/history", "/account",
		"/admin/users", "/admin/users/new", "/admin/users/1/edit", "/admin/settings", "/offline",
	} {
		c.get(p, 200)
	}

	// Room numbers are shown and the dashboard goes room by room, with
	// things for the whole building after its rooms.
	c.post("/places/new?parent=2", "/places", url.Values{"kind": {"room"}, "parent_id": {"2"}, "number": {"9"}, "name": {"Office"}}, 200)
	if !strings.Contains(c.get("/places/4", 200), "9 – Office") {
		t.Error("room page should show the room number")
	}
	c.post("/items/new", "/items", url.Values{"place_id": {"4"}, "name": {"Thermostat"}}, 200)
	c.post("/tasks/new?item=3", "/tasks", url.Values{"item_id": {"3"}, "name": {"Replace batteries"}}, 200)
	if d := c.get("/", 200); strings.Index(d, "Replace batteries") > strings.Index(d, "Inspect shingles") {
		t.Error("dashboard should list numbered rooms before building-wide items")
	}

	// Bulk add rooms: all-or-nothing, with per-line errors.
	if body := c.post("/places/bulk?parent=2", "/places/bulk", url.Values{"parent_id": {"2"}, "kind": {"room"}, "lines": {"201, Library\n202,"}}, 422); !strings.Contains(body, "Line 2") {
		t.Error("expected per-line error for missing room name")
	}
	if tree, _ := st.Places(); len(tree.Children(2)) != 2 {
		t.Fatalf("failed bulk add should not save any rooms, got %d", len(tree.Children(2)))
	}
	body := c.post("/places/bulk?parent=2", "/places/bulk", url.Values{
		"parent_id": {"2"}, "kind": {"room"}, "lines": {"201, Library\r\n\r\n202\tChoir Room\nAttic\n"},
	}, 200)
	for _, want := range []string{"3 rooms added", "201 – Library", "202 – Choir Room", "Attic"} {
		if !strings.Contains(body, want) {
			t.Errorf("building page after bulk add missing %q", want)
		}
	}
	if p, _ := st.GetPlace(6); p.Kind != "room" || p.ParentID != 2 || p.Number != "202" || p.Name != "Choir Room" {
		t.Errorf("bulk room not saved correctly: %+v", p)
	}

	// Supplies: add one to a room, use and restock from the room page.
	c.post("/supplies/new?place=3", "/supplies", url.Values{
		"place_id": {"3"}, "name": {"Diapers"}, "unit": {"packs"}, "quantity": {"2"}, "reorder_at": {"1"},
	}, 200)
	for _, p := range []string{"/supplies", "/supplies?low=1&place=2&q=diap", "/supplies/new", "/supplies/1", "/supplies/1/edit"} {
		c.get(p, 200)
	}
	if body := c.get("/places/3", 200); !strings.Contains(body, "Diapers") || !strings.Contains(body, "2 packs") {
		t.Error("room page should list its supplies")
	}
	body = c.post("/places/3", "/supplies/1/adjust", url.Values{"kind": {"used"}, "amount": {"1"}, "next": {"/places/3"}}, 200)
	if !strings.Contains(body, "1 packs on hand. Time to reorder.") {
		t.Error("using a supply should flash the new count and low warning")
	}
	if !strings.Contains(c.get("/", 200), "Supplies to restock") {
		t.Error("dashboard should list low supplies")
	}
	if body := c.post("/places/3", "/supplies/1/adjust", url.Values{"kind": {"used"}, "amount": {"5"}, "next": {"/places/3"}}, 200); !strings.Contains(body, "has only 1 on hand") {
		t.Error("using more than on hand should be rejected")
	}
	c.post("/supplies/1", "/supplies/1/adjust", url.Values{"kind": {"restocked"}, "amount": {"6"}, "note": {"Costco run"}}, 200)
	c.post("/supplies/1", "/supplies/1/adjust", url.Values{"kind": {"counted"}, "amount": {"0"}}, 200)
	c.post("/supplies/1", "/supplies/1/adjust", url.Values{"kind": {"used"}, "amount": {"0"}}, 200)
	if body := c.get("/supplies/1", 200); !strings.Contains(body, "Costco run") || !strings.Contains(body, "Recounted") {
		t.Error("supply history missing entries")
	}
	if sp, _ := st.GetSupply(1); sp.Quantity != 0 {
		t.Errorf("quantity = %d, want 0", sp.Quantity)
	}
	// Editing details must not change the count.
	c.post("/supplies/1/edit", "/supplies/1", url.Values{
		"place_id": {"3"}, "name": {"Diapers (size 3)"}, "quantity": {"99"},
	}, 200)
	if sp, _ := st.GetSupply(1); sp.Quantity != 0 || sp.Name != "Diapers (size 3)" {
		t.Errorf("edit changed supply unexpectedly: %+v", sp)
	}

	// Reusable supplies: swap from the room page, then get them back.
	c.post("/supplies/new", "/supplies", url.Values{
		"place_id": {"3"}, "name": {"Mop heads"}, "reusable": {"1"},
		"quantity": {"3"}, "in_use": {"1"}, "cleaning": {"0"},
	}, 200)
	body = c.post("/places/3", "/supplies/2/adjust", url.Values{"kind": {"swapped"}, "amount": {"1"}, "next": {"/places/3"}}, 200)
	if !strings.Contains(body, "Mop heads: 2 clean · 1 in use · 1 cleaning.") {
		t.Error("swap should flash the new breakdown")
	}
	c.post("/supplies/2", "/supplies/2/adjust", url.Values{"kind": {"returned"}, "amount": {"1"}}, 200)
	if body := c.get("/supplies/2", 200); !strings.Contains(body, "Back from cleaning") || !strings.Contains(body, "Swapped dirty for clean") {
		t.Error("reusable history missing entries")
	}
	if sp, _ := st.GetSupply(2); sp.Quantity != 3 || sp.InUse != 1 || sp.Cleaning != 0 {
		t.Errorf("mop heads = %+v", sp)
	}
	// Reusable-only actions are refused for consumables.
	c.post("/supplies/1", "/supplies/1/adjust", url.Values{"kind": {"swapped"}, "amount": {"1"}}, 200)

	// A task that uses a supply: linked on the task form, taken when done.
	c.post("/supplies/new", "/supplies", url.Values{"place_id": {"2"}, "name": {"Filters 20x25x1"}, "quantity": {"1"}}, 200)
	c.post("/tasks/new?item=2", "/tasks", url.Values{"item_id": {"2"}, "name": {"Replace filter"}, "uses_supply": {"1"}, "supply_id": {"2"}, "supply_amount": {"1"}}, 422)
	c.post("/tasks/new?item=2", "/tasks", url.Values{"item_id": {"2"}, "name": {"Replace filter"}, "uses_supply": {"1"}, "supply_id": {"3"}, "supply_amount": {"1"},
		"recurring": {"1"}, "interval_value": {"3"}, "interval_unit": {"months"}}, 200)
	if body := c.get("/items/2", 200); !strings.Contains(body, "Supplies it uses") || !strings.Contains(body, "Filters 20x25x1") {
		t.Error("item page should list the supplies its tasks use")
	}
	if body := c.get("/tasks/4/complete", 200); !strings.Contains(body, "Took it from supplies") {
		t.Error("completion form should offer to take the supply")
	}
	c.post("/tasks/4/complete", "/tasks/4/complete", url.Values{"performed_on": {"2026-01-10"}, "took_supply": {"1"}, "supply_amount": {"1"}}, 200)
	if sp, _ := st.GetSupply(3); sp.Quantity != 0 {
		t.Errorf("filters on hand = %d, want 0", sp.Quantity)
	}
	if body := c.post("/tasks/4/complete", "/tasks/4/complete", url.Values{"performed_on": {"2026-01-11"}, "took_supply": {"1"}, "supply_amount": {"1"}}, 422); !strings.Contains(body, "only 0 on hand") {
		t.Error("completing without enough supply should explain why")
	}
	// Unticked: recorded without touching the count.
	c.post("/tasks/4/complete", "/tasks/4/complete", url.Values{"performed_on": {"2026-01-11"}}, 200)
	// A "sometimes" task (check, replace only if needed) links the supply but
	// doesn't pre-tick taking it; ticking it still takes one.
	c.post("/supplies/3", "/supplies/3/adjust", url.Values{"kind": {"restocked"}, "amount": {"2"}}, 200)
	c.post("/tasks/new?item=2", "/tasks", url.Values{"item_id": {"2"}, "name": {"Check filter"}, "uses_supply": {"1"}, "supply_id": {"3"},
		"supply_amount": {"1"}, "supply_always": {"0"}, "recurring": {"1"}, "interval_value": {"1"}, "interval_unit": {"months"}}, 200)
	if task, _ := st.GetTask(5); task.SupplyAlways || task.SupplyID != 3 {
		t.Fatalf("check task = %+v", task)
	}
	form := c.get("/tasks/5/complete", 200)
	if !strings.Contains(form, "Replaced it this time") || strings.Contains(form, `name="took_supply" value="1" checked`) {
		t.Error("sometimes-task completion should offer, but not pre-tick, taking the supply")
	}
	if !strings.Contains(c.get("/items/2", 200), "May need 1 ×") {
		t.Error("sometimes-task card should say it may need the supply")
	}
	c.post("/tasks/5/complete", "/tasks/5/complete", url.Values{"performed_on": {"2026-01-12"}}, 200)
	if sp, _ := st.GetSupply(3); sp.Quantity != 2 {
		t.Errorf("check without replacing should not use a filter; on hand = %d", sp.Quantity)
	}
	c.post("/tasks/5/complete", "/tasks/5/complete", url.Values{"performed_on": {"2026-01-13"}, "took_supply": {"1"}, "supply_amount": {"1"}}, 200)
	if sp, _ := st.GetSupply(3); sp.Quantity != 1 {
		t.Errorf("check with replacing should use a filter; on hand = %d", sp.Quantity)
	}
	if !strings.Contains(c.get("/tasks/4/complete", 200), `name="took_supply" value="1" checked`) {
		t.Error("every-time task should pre-tick taking the supply")
	}

	if body := c.get("/supplies/3", 200); !strings.Contains(body, "Used by") || !strings.Contains(body, "Replace filter (Roof, Sanctuary)") {
		t.Error("supply page should list tasks using it and the history note")
	}

	dash := c.get("/", 200)
	for _, want := range []string{"Test alarm", "Overdue", "Mark done", "Grace Church", "Inspect shingles"} {
		if !strings.Contains(dash, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}

	c.post("/tasks/1/complete", "/tasks/1/complete", url.Values{
		"performed_on": {"2026-01-10"}, "performed_by": {"Bob"}, "cost": {"$12.50"}, "next": {"/"},
	}, 200)
	task, _ = st.GetTask(1)
	if task.LastCompletedOn != "2026-01-10" || task.NextDueOn != "2026-07-10" {
		t.Fatalf("task not rolled forward: %+v", task)
	}
	if !strings.Contains(c.get("/items/1", 200), "$12.50") {
		t.Error("cost not shown in history")
	}

	// Future dates are rejected.
	c.post("/tasks/1/complete", "/tasks/1/complete", url.Values{"performed_on": {"2999-01-01"}}, 422)

	// Backup is a SQLite file.
	resp, err := c.http.Get(c.base + "/admin/backup")
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 16)
	io.ReadFull(resp.Body, head)
	resp.Body.Close()
	if string(head) != "SQLite format 3\x00" {
		t.Fatalf("backup is not a sqlite db: %q", head)
	}

	// Sign out: public dashboard still works, edit pages redirect to login.
	c.post("/", "/logout", url.Values{}, 200)
	dash = c.get("/", 200)
	if strings.Contains(dash, "Mark done") || !strings.Contains(dash, "Test alarm") {
		t.Error("public dashboard should show tasks without edit actions")
	}
	if body := c.get("/places", 200); !strings.Contains(body, "Sign in") {
		t.Error("expected login page")
	}
}

func TestCSRFRejected(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("admin", "", "a long password", true)
	c.get("/login", 200) // sets csrf cookie
	resp, err := c.http.PostForm(c.base+"/login", url.Values{"username": {"admin"}, "password": {"a long password"}, "_csrf": {"forged"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
}

func TestLoginAndRateLimit(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("admin", "", "a long password", true)
	for i := 0; i < 10; i++ {
		c.post("/login", "/login", url.Values{"username": {"admin"}, "password": {"nope"}}, 401)
	}
	body := c.post("/login", "/login", url.Values{"username": {"admin"}, "password": {"a long password"}}, 401)
	if !strings.Contains(body, "Too many") {
		t.Fatal("expected rate limit message")
	}
}

func TestStaticAndPWA(t *testing.T) {
	c, _ := newTestServer(t)
	for _, p := range []string{"/manifest.webmanifest", "/sw.js", "/static/js/application.js", "/static/js/stimulus_autoloader.js",
		"/static/js/controllers/pwa/install_controller.js", "/static/css/app.css", "/static/icons/icon-192.png", "/healthz"} {
		c.get(p, 200)
	}
	sw := c.get("/sw.js", 200)
	if strings.Contains(sw, "__VERSION__") || strings.Contains(sw, "__PRECACHE__") {
		t.Fatal("service worker placeholders not replaced")
	}
	c.get("/static/", 404)
	c.get("/nope", 404)
}

func TestSafeRedirect(t *testing.T) {
	for in, want := range map[string]string{
		"":                "/",
		"/items/1":        "/items/1",
		"//evil.com":      "/",
		"https://evil":    "/",
		"/\\evil.com":     "/",
		"/?building=2":    "/?building=2",
		"/items/../admin": "/admin",
	} {
		if got := safeRedirect(in); got != want {
			t.Errorf("safeRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseMoney(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "12": 1200, "12.5": 1250, "$1,200.05": 120005, "0.99": 99} {
		if got, ok := parseMoney(in); !ok || got != want {
			t.Errorf("parseMoney(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"abc", "1.234", "-5"} {
		if _, ok := parseMoney(bad); ok {
			t.Errorf("parseMoney(%q) should fail", bad)
		}
	}
}

func TestCountedItems(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"username": {"admin"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	addPlaces(c, "building:Main", "room:2:104:Nursery", "room:2::Gym") // places 2, 3, 4
	c.post("/supplies/new", "/supplies", url.Values{"place_id": {"2"}, "name": {"LED A19"}, "unit": {"bulbs"}, "quantity": {"3"}, "reorder_at": {"1"}}, 200)
	c.post("/supplies/new", "/supplies", url.Values{"place_id": {"2"}, "name": {"Mop heads"}, "reusable": {"1"}, "quantity": {"2"}}, 200)

	item := func(name, category, quantity string, extra url.Values) url.Values {
		v := url.Values{"place_id": {"3"}, "name": {name}, "category": {category}, "quantity": {quantity}, "supply_source": {"none"}}
		for k, vs := range extra {
			v[k] = vs
		}
		return v
	}

	// Plain counts: 10 outlets, 3 switches.
	c.post("/items/new?place=3", "/items", item("Electrical outlets", "Electrical", "10", nil), 200)
	c.post("/items/new?place=3", "/items", item("Light switches", "Electrical", "3", nil), 200)
	if it, _ := st.GetItem(2); it.Quantity != 3 || it.SupplyID != 0 {
		t.Errorf("switches = %+v", it)
	}
	// 15 lights using a bulb from supplies.
	c.post("/items/new?place=3", "/items", item("Lights", "Lighting", "15", url.Values{"supply_source": {"existing"}, "supply_id": {"1"}, "supply_per": {"1"}}), 200)
	if body := c.post("/items/new", "/items", item("Lights", "", "1", url.Values{"supply_source": {"existing"}, "supply_id": {"2"}, "supply_per": {"1"}}), 422); !strings.Contains(body, "reusable") {
		t.Error("expected reusable supply to be refused")
	}
	// 2 air returns using a new filter supply, added to the building.
	c.post("/items/new?place=3", "/items", item("Air returns", "HVAC", "2", url.Values{"supply_source": {"new"}, "new_supply_name": {"Air filters 12x20x1"},
		"new_supply_unit": {"filters"}, "new_supply_quantity": {"1"}, "supply_per": {"1"}}), 200)
	if sp, err := st.GetSupply(3); err != nil || sp.Name != "Air filters 12x20x1" || sp.Unit != "filters" || sp.Quantity != 1 || sp.PlaceID != 2 {
		t.Fatalf("new filter supply = %+v, %v", sp, err)
	}
	if it, _ := st.GetItem(4); it.SupplyID != 3 || it.SupplyTotal() != 2 {
		t.Errorf("air returns = %+v", it)
	}
	// A portable projector, and a blank count meaning 1.
	c.post("/items/new?place=3", "/items", item("Projector", "Audio/Visual", "", url.Values{"portable": {"1"}}), 200)
	if it, _ := st.GetItem(5); it.Quantity != 1 || !it.Portable {
		t.Errorf("projector = %+v", it)
	}
	if body := c.post("/items/new", "/items", url.Values{"place_id": {"2"}, "quantity": {"0"}, "supply_source": {"new"}}, 422); !strings.Contains(body, "Name is required") ||
		!strings.Contains(body, "How many must be 1") || !strings.Contains(body, "name for the new supply") {
		t.Error("expected name, count and supply validation errors")
	}

	// The room lists everything grouped by category with quick actions.
	room := c.get("/places/3", 200)
	for _, want := range []string{"Audio/Visual", "Electrical", "HVAC", "Lighting", "Electrical outlets</strong></a> <span class=\"qty\">× 10", "LED A19", "/items/3/replace", "/items/5/move"} {
		if !strings.Contains(room, want) {
			t.Errorf("room page missing %q", want)
		}
	}
	if strings.Index(room, ">Audio/Visual <") > strings.Index(room, ">Lighting <") {
		t.Error("categories should be in alphabetical order")
	}
	if strings.Contains(room, "/items/1/replace") || strings.Contains(room, "/items/1/move") {
		t.Error("outlets have no supply and aren't portable, so no quick actions")
	}

	for _, p := range []string{"/items/new", "/items/3", "/items/3/edit", "/items/3/replace", "/items/5/move", "/items", "/places/2"} {
		c.get(p, 200)
	}
	if body := c.get("/supplies/1", 200); !strings.Contains(body, "Items that use it") || !strings.Contains(body, "15, 1 each (15 to replace them all)") {
		t.Error("supply page should list the items that use it")
	}
	// New tasks start from the item's supply, enough for all of them.
	if !strings.Contains(c.get("/tasks/new?item=4", 200), `name="supply_amount" type="number" min="1" step="1" inputmode="numeric" value="2"`) {
		t.Error("task form should default to the item's supply for all of them")
	}
	if body := c.get("/items/1/replace", 200); !strings.Contains(body, "Set the supply") {
		t.Error("replacing on an item without a supply should send you to set one")
	}
	if !strings.Contains(c.get("/items/3/replace", 200), `name="took_supply" value="1" checked`) {
		t.Error("replace form should pre-tick taking from supplies when enough is on hand")
	}

	// Replacing takes from supplies and returns to the room.
	body := c.post("/items/3/replace", "/items/3/replace", url.Values{"performed_on": {"2026-01-10"}, "replaced": {"2"}, "took_supply": {"1"}, "next": {"/places/3"}}, 200)
	if !strings.Contains(body, "Recorded 2 × LED A19 replaced in Lights. 1 bulbs on hand. Time to reorder.") || !strings.Contains(body, "Last replaced Jan 10, 2026") {
		t.Error("replacing should flash the new count and show the date on the room page")
	}
	if body := c.post("/items/3/replace", "/items/3/replace", url.Values{"performed_on": {"2026-01-11"}, "replaced": {"5"}, "took_supply": {"1"}}, 422); !strings.Contains(body, "only 1 on hand") {
		t.Error("taking more than on hand should explain why")
	}
	c.post("/items/3/replace", "/items/3/replace", url.Values{"performed_on": {"2026-01-11"}, "replaced": {"5"}}, 200)
	c.post("/items/3/replace", "/items/3/replace", url.Values{"performed_on": {"2999-01-01"}, "replaced": {"1"}}, 422)
	if sp, _ := st.GetSupply(1); sp.Quantity != 1 {
		t.Errorf("bulbs on hand = %d, want 1", sp.Quantity)
	}
	body = c.get("/items/3", 200)
	for _, want := range []string{"Replaced 2 × LED A19", "Took 2 × LED A19 from supplies", "Replaced 5 × LED A19", "Not taken from supplies", "Jan 11, 2026"} {
		if !strings.Contains(body, want) {
			t.Errorf("item history missing %q", want)
		}
	}
	if !strings.Contains(c.get("/supplies/1", 200), "Replaced in Lights, 104 – Nursery") {
		t.Error("supply history should say which item it went to")
	}
	// A task taking the item's own supply counts as replacing it.
	c.post("/tasks/new?item=4", "/tasks", url.Values{"item_id": {"4"}, "name": {"Check filters"}, "uses_supply": {"1"}, "supply_id": {"3"},
		"supply_amount": {"2"}, "supply_always": {"0"}, "recurring": {"1"}, "interval_value": {"1"}, "interval_unit": {"months"}}, 200)
	c.post("/tasks/1/complete", "/tasks/1/complete", url.Values{"performed_on": {"2026-02-01"}, "took_supply": {"1"}, "supply_amount": {"1"}}, 200)
	if it, _ := st.GetItem(4); it.LastReplaced != "2026-02-01" || it.Supply.Quantity != 0 {
		t.Errorf("air returns after check = %+v", it)
	}

	// Deleting a replacement puts back what it took.
	logs, _ := st.ListLogs(store.LogFilter{ItemID: 3})
	first := logs[len(logs)-1]
	c.post("/items/3", fmt.Sprintf("/logs/%d/delete", first.ID), url.Values{}, 200)
	if sp, _ := st.GetSupply(1); sp.Quantity != 3 {
		t.Errorf("bulbs on hand after delete = %d, want 3", sp.Quantity)
	}

	// Moving a portable item records where it went.
	body = c.post("/items/5/move", "/items/5/move", url.Values{"place_id": {"4"}, "moved_on": {"2026-03-01"}, "note": {"Youth night"}, "next": {"/items/5"}}, 200)
	if !strings.Contains(body, "Projector moved to Main › Gym.") || !strings.Contains(body, "Moved from Main › 104 – Nursery to Main › Gym.") || !strings.Contains(body, "Youth night") {
		t.Error("move should flash and appear in history")
	}
	if body := c.post("/items/5/move", "/items/5/move", url.Values{"place_id": {"4"}, "moved_on": {"2026-03-02"}}, 422); !strings.Contains(body, "where it is now") {
		t.Error("moving to the same place should be refused")
	}
	// Changing the place on the edit form is recorded as a move too.
	c.post("/items/5/edit", "/items/5", item("Projector", "Audio/Visual", "1", url.Values{"portable": {"1"}}), 200)
	if logs, _ := st.ListLogs(store.LogFilter{ItemID: 5}); len(logs) != 2 || logs[0].Kind != "moved" {
		t.Errorf("edit should record a move: %+v", logs)
	}
	if !strings.Contains(c.get("/history", 200), "Moved") {
		t.Error("site history should include moves")
	}
}

func TestProblems(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"username": {"admin"}, "display_name": {"Alex"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	addPlaces(c, "building:Main", "room:2:110:Kitchen") // places 2, 3
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Sink"}}, 200)

	if body := c.get("/report?item=1", 200); !strings.Contains(body, `<input type="hidden" name="item_id" value="1">`) ||
		!strings.Contains(body, `<input type="hidden" name="place_id" value="3">`) || !strings.Contains(body, "<strong>Sink</strong>") {
		t.Error("report form should be about the item")
	}
	body := c.post("/report", "/report", url.Values{"place_id": {"2"}, "item_id": {"1"}, "title": {"Faucet dripping"}, "details": {"Hot side"}}, 200)
	for _, want := range []string{"Problem reported.", "Faucet dripping", "Nobody is on it yet", "by Alex", "Sink"} {
		if !strings.Contains(body, want) {
			t.Errorf("problem page missing %q", want)
		}
	}
	if p, _ := st.GetProblem(1); p.PlaceID != 3 {
		t.Errorf("naming the item should put the problem where it is: place %d", p.PlaceID)
	}
	for _, p := range []string{"/", "/places/3", "/items/1", "/places/2", "/places/1", "/problems"} {
		if !strings.Contains(c.get(p, 200), "Faucet dripping") {
			t.Errorf("%s should list the open problem", p)
		}
	}
	for _, p := range []string{"/problems/1/edit", "/problems?status=all&place=2&mine=1", "/report?place=3", "/admin/settings"} {
		c.get(p, 200)
	}

	// "I'm on it" assigns it to me and starts it.
	body = c.post("/problems/1", "/problems/1/update", url.Values{"status": {"in_progress"}, "assigned_to": {"1"}}, 200)
	if !strings.Contains(body, "It&#39;s yours") || !strings.Contains(body, "Alex is on it") || strings.Contains(body, "I&#39;m on it</button>") {
		t.Error("taking a problem should assign it and hide the button")
	}
	if body := c.post("/problems/1", "/problems/1/update", url.Values{"status": {"in_progress"}, "assigned_to": {"1"}}, 200); !strings.Contains(body, "Nothing changed.") {
		t.Error("an update with no changes should say so")
	}
	body = c.post("/problems/1", "/problems/1/update", url.Values{"status": {"resolved"}, "assigned_to": {"1"}, "note": {"Replaced washer"}}, 200)
	if !strings.Contains(body, "Marked resolved.") || !strings.Contains(body, "Replaced washer") {
		t.Error("resolving should show in the timeline")
	}
	if p, _ := st.GetProblem(1); p.ResolvedAt == "" {
		t.Error("resolved_at not set")
	}
	if strings.Contains(c.get("/problems", 200), "Faucet dripping") || !strings.Contains(c.get("/problems?status=resolved", 200), "Faucet dripping") {
		t.Error("resolved problems should only be listed when asked for")
	}
	// Reopening clears the resolved time; unassigning shows in the timeline.
	body = c.post("/problems/1", "/problems/1/update", url.Values{"status": {"open"}, "assigned_to": {"0"}}, 200)
	if p, _ := st.GetProblem(1); p.ResolvedAt != "" || p.AssignedTo != 0 || p.Status != "open" {
		t.Errorf("reopened problem = %+v", p)
	}
	if !strings.Contains(body, "Unassigned") {
		t.Error("timeline should show unassigning")
	}
	c.post("/problems/1", "/problems/1/update", url.Values{"status": {"bogus"}, "assigned_to": {"0"}}, 200)
	c.post("/problems/1/edit", "/problems/1", url.Values{"place_id": {"2"}, "title": {"Kitchen faucet dripping"}, "reporter_name": {"Alex"}}, 200)
	if p, _ := st.GetProblem(1); p.Title != "Kitchen faucet dripping" || p.PlaceID != 2 || p.ItemID != 0 {
		t.Errorf("edited problem = %+v", p)
	}

	// Public reporting is off by default: visitors are asked to sign in,
	// and the public dashboard doesn't show reports.
	c.post("/", "/logout", url.Values{}, 200)
	if body := c.get("/report", 200); !strings.Contains(body, "Sign in") || strings.Contains(body, "Your name") {
		t.Error("report page should require sign-in while public reports are off")
	}
	if strings.Contains(c.get("/", 200), "Kitchen faucet dripping") {
		t.Error("public dashboard should not list problems")
	}

	c.post("/login", "/login", url.Values{"username": {"admin"}, "password": {"a long password"}}, 200)
	c.post("/admin/settings", "/admin/settings", url.Values{"site_name": {"Main"}, "due_soon_days": {"30"}, "public_reports": {"1"}, "public_item_reports": {"1"}}, 200)
	if !strings.Contains(c.get("/admin/settings", 200), `name="public_reports" value="1" checked`) {
		t.Error("settings should show public reports turned on")
	}
	c.post("/", "/logout", url.Values{}, 200)

	body = c.get("/report?place=3", 200)
	if !strings.Contains(body, "Your name") || !strings.Contains(body, `href="/report"`) {
		t.Error("visitors should get the report form and a nav link to it")
	}
	if body := c.post("/report", "/report", url.Values{"place_id": {"2"}, "title": {"Light out"}}, 422); !strings.Contains(body, "Enter your name") {
		t.Error("visitors must give their name")
	}
	report := url.Values{"place_id": {"3"}, "title": {"Light out"}, "reporter_name": {"Pat"}, "reporter_contact": {"555-1234"}, "item_id": {"1"}}
	if body := c.post("/report", "/report", report, 200); !strings.Contains(body, "Thanks!") {
		t.Error("visitor report should thank them")
	}
	if p, _ := st.GetProblem(2); p.ReporterName != "Pat" || p.ReporterContact != "555-1234" || p.ReportedBy != 0 || p.ItemID != 1 || p.PlaceID != 3 {
		t.Errorf("visitor report = %+v", p)
	}
	// Honeypot: looks like it worked, saves nothing.
	spam := url.Values{"website": {"http://spam.example"}}
	for k, v := range report {
		spam[k] = v
	}
	c.post("/report", "/report", spam, 200)
	if ps, _ := st.ListProblems(store.ProblemFilter{}); len(ps) != 2 {
		t.Errorf("honeypot report was saved: %d problems", len(ps))
	}
	// 10 reports an hour per device.
	for i := 0; i < 9; i++ {
		c.post("/report", "/report", report, 200)
	}
	if body := c.post("/report", "/report", report, 429); !strings.Contains(body, "Too many reports") {
		t.Error("expected report rate limit")
	}
	// Visitors still can't see reports.
	if body := c.get("/problems/2", 200); !strings.Contains(body, "Sign in") || strings.Contains(body, "555-1234") {
		t.Error("problem pages must require sign-in")
	}
}

func TestUnits(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"username": {"admin"}, "display_name": {"Alex"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	addPlaces(c, "building:Main", "room:2::Hall") // places 2, 3
	c.post("/supplies/new", "/supplies", url.Values{"place_id": {"2"}, "name": {"LED A19"}, "unit": {"bulbs"}, "quantity": {"10"}}, 200)
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Lights"}, "quantity": {"4"},
		"supply_source": {"existing"}, "supply_id": {"1"}, "supply_per": {"1"}}, 200)

	// Location notes for the numbered units.
	c.get("/items/1/units", 200)
	c.post("/items/1/units", "/items/1/units", url.Values{"label_3": {"Over the stage"}, "label_9": {"ignored"}}, 200)
	body := c.get("/items/1", 200)
	if !strings.Contains(body, "<strong>#3</strong> <span class=\"muted\">– Over the stage</span>") || strings.Contains(body, "ignored") {
		t.Error("units table should show notes for #1–#4 only")
	}

	// Replacing records which ones; units outside 1..4 are dropped.
	if !strings.Contains(c.get("/items/1/replace", 200), `name="units" value="4"`) {
		t.Error("replace form should offer the unit picker")
	}
	body = c.post("/items/1/replace", "/items/1/replace", url.Values{"performed_on": {"2026-01-10"}, "replaced": {"2"}, "took_supply": {"1"},
		"units": {"2", "3", "7"}}, 200)
	if !strings.Contains(body, "Recorded 2 × LED A19 replaced in Lights #2, #3.") || !strings.Contains(body, "· #2, #3") {
		t.Error("replacement should name the units in the flash and history")
	}
	if !strings.Contains(c.get("/supplies/1", 200), "Replaced in Lights, Hall, #2, #3") {
		t.Error("supply history should name the units")
	}
	// #3 replaced three times in a year gets flagged.
	for _, day := range []string{"2026-03-01", "2026-05-01"} {
		c.post("/items/1/replace", "/items/1/replace", url.Values{"performed_on": {day}, "replaced": {"1"}, "units": {"3"}}, 200)
	}
	units, _ := st.ItemUnits(1, 4, mustDate(t, "2026-06-01"))
	if units[2].RecentCount != 3 || !units[2].Frequent() || units[2].LastReplaced != "2026-05-01" || units[1].RecentCount != 1 || units[0].LastReplaced != "" {
		t.Errorf("units = %+v", units)
	}
	if !strings.Contains(c.get("/items/1", 200), "3 times: check why") {
		t.Error("item page should flag the unit replaced too often")
	}

	// Mark done and other work can record units too.
	c.post("/tasks/new?item=1", "/tasks", url.Values{"item_id": {"1"}, "name": {"Clean fixtures"}}, 200)
	if !strings.Contains(c.get("/tasks/1/complete", 200), `name="units" value="1"`) {
		t.Error("completion form should offer the unit picker")
	}
	c.post("/tasks/1/complete", "/tasks/1/complete", url.Values{"performed_on": {"2026-05-02"}, "units": {"1", "4"}}, 200)
	c.post("/items/1/log", "/items/1/log", url.Values{"performed_on": {"2026-05-03"}, "notes": {"Tightened socket"}, "units": {"3"}}, 200)
	logs, _ := st.ListLogs(store.LogFilter{ItemID: 1})
	if logs[0].UnitsLabel() != "#3" || logs[1].UnitsLabel() != "#1, #4" {
		t.Errorf("log units = %q, %q", logs[0].UnitsLabel(), logs[1].UnitsLabel())
	}

	// A problem reported against the group, narrowed to one unit by staff,
	// then fixed from the problem page.
	c.post("/report", "/report", url.Values{"place_id": {"3"}, "item_id": {"1"}, "title": {"Light out"}}, 200)
	body = c.post("/problems/1", "/problems/1/unit", url.Values{"unit": {"3"}}, 200)
	if !strings.Contains(body, "Noted: it&#39;s #3.") || !strings.Contains(body, "It&#39;s #3 – Over the stage.") || !strings.Contains(body, "Replace #3") {
		t.Error("setting the unit should show in the timeline and offer to replace it")
	}
	if p, _ := st.GetProblem(1); p.WhatLabel() != "Lights #3 – Over the stage" {
		t.Errorf("what = %q", p.WhatLabel())
	}
	if !strings.Contains(c.get("/problems", 200), "Lights #3 – Over the stage") {
		t.Error("problem list should show which unit")
	}
	c.post("/problems/1", "/problems/1/unit", url.Values{"unit": {"9"}}, 200)
	if p, _ := st.GetProblem(1); p.Unit != 3 {
		t.Errorf("out-of-range unit was saved: %d", p.Unit)
	}
	if !strings.Contains(c.get("/items/1/replace?unit=3&problem=1&next=/problems/1", 200), `name="units" value="3" data-unit-count-target="unit" data-action="unit-count#update" checked`) {
		t.Error("replace from a problem should pre-tick its unit")
	}
	body = c.post("/items/1/replace", "/items/1/replace", url.Values{"performed_on": {"2026-05-04"}, "replaced": {"1"}, "took_supply": {"1"}, "units": {"3"},
		"problem": {"1"}, "next": {"/problems/1"}}, 200)
	if !strings.Contains(body, "Problem &#34;Light out&#34; marked resolved.") || !strings.Contains(body, "Replaced 1 × LED A19 in Lights #3.") {
		t.Error("replacing from the problem page should resolve it with a note")
	}
	if p, _ := st.GetProblem(1); p.Status != store.ProblemResolved || p.AssignedTo != 1 {
		t.Errorf("problem after fix = %+v", p)
	}
	// Log work from a problem resolves it too.
	c.post("/report", "/report", url.Values{"place_id": {"2"}, "item_id": {"1"}, "title": {"Buzzing"}}, 200)
	c.post("/items/1/log?problem=2", "/items/1/log", url.Values{"performed_on": {"2026-05-05"}, "notes": {"Replaced dimmer"}, "problem": {"2"}}, 200)
	if p, _ := st.GetProblem(2); p.Status != store.ProblemResolved {
		t.Errorf("problem 2 should be resolved: %+v", p)
	}

	// Reporting one unit: picked on the report form, from the item's page,
	// or from its report link; leaving it out reports the group.
	if body := c.get("/report", 200); !strings.Contains(body, `<option value="3" data-item="1" >#3 – Over the stage</option>`) {
		t.Error("report form should offer the item's units")
	}
	if body := c.get("/report?item=1", 200); !strings.Contains(body, `<option value="4" >#4</option>`) {
		t.Error("an item's report form should ask which one")
	}
	if !strings.Contains(c.get("/items/1", 200), `href="/report?item=1&amp;unit=3"`) {
		t.Error("each unit should have a report link")
	}
	c.post("/report", "/report", url.Values{"place_id": {"3"}, "item_id": {"1"}, "unit": {"4"}, "title": {"Flickers"}}, 200)
	c.post("/report", "/report", url.Values{"place_id": {"3"}, "item_id": {"1"}, "unit": {"0"}, "title": {"Two are out"}}, 200)
	c.post("/report", "/report", url.Values{"place_id": {"3"}, "item_id": {"1"}, "unit": {"9"}, "title": {"Bad unit"}}, 200)
	for id, want := range map[int64]int{3: 4, 4: 0, 5: 0} {
		if p, _ := st.GetProblem(id); p.Unit != want {
			t.Errorf("problem %d unit = %d, want %d", id, p.Unit, want)
		}
	}
	// Editing can say which one, noted in the timeline, or change its mind.
	edit := url.Values{"place_id": {"3"}, "item_id": {"1"}, "unit": {"2"}, "title": {"Two are out"}, "reporter_name": {"Alex"}}
	if body := c.post("/problems/4/edit", "/problems/4", edit, 200); !strings.Contains(body, "It&#39;s #2.") {
		t.Error("choosing the unit on edit should show in the timeline")
	}
	if !strings.Contains(c.get("/problems/4/edit", 200), `<option value="2" data-item="1" selected>#2</option>`) {
		t.Error("edit form should show the chosen unit")
	}
	edit.Set("unit", "0")
	c.post("/problems/4/edit", "/problems/4", edit, 200)
	if p, _ := st.GetProblem(4); p.Unit != 0 {
		t.Errorf("clearing the unit on edit: %d", p.Unit)
	}

	// Fewer units: #3 and #4 drop out of pickers, their history stays.
	c.post("/items/1/edit", "/items/1", url.Values{"place_id": {"3"}, "name": {"Lights"}, "quantity": {"2"},
		"supply_source": {"existing"}, "supply_id": {"1"}, "supply_per": {"1"}}, 200)
	if strings.Contains(c.get("/items/1/replace", 200), `name="units" value="3"`) {
		t.Error("units beyond the count should not be offered")
	}
	if !strings.Contains(c.get("/items/1", 200), "· #1, #4") {
		t.Error("history should keep units no longer counted")
	}
	// Single items have no units.
	c.post("/items/new", "/items", url.Values{"place_id": {"2"}, "name": {"Furnace"}}, 200)
	if strings.Contains(c.get("/items/2/log", 200), "Which ones") {
		t.Error("single items should not offer a unit picker")
	}
}

func TestMoveSome(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"username": {"admin"}, "display_name": {"Alex"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	addPlaces(c, "building:Main", "room:2::Hall", "room:2::Gym") // places 2, 3, 4
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Chairs"}, "quantity": {"10"}, "portable": {"1"}}, 200)

	body := c.get("/items/1/move", 200)
	if !strings.Contains(body, `name="units" value="10"`) || !strings.Contains(body, `name="count" type="number" min="1" max="10" step="1" inputmode="numeric" value="10"`) {
		t.Error("move form should offer the unit picker and default to all of them")
	}
	if body := c.post("/items/1/move", "/items/1/move", url.Values{"place_id": {"4"}, "moved_on": {"2026-03-01"}, "count": {"11"}}, 422); !strings.Contains(body, "How many must be between 1 and 10.") {
		t.Error("moving more than there are should be refused")
	}
	body = c.post("/items/1/move", "/items/1/move", url.Values{"place_id": {"4"}, "moved_on": {"2026-03-01"}, "count": {"10"}, "units": {"2", "5"}}, 200)
	if !strings.Contains(body, "Moved 2 of 10 Chairs to Main › Gym; 8 left here.") || !strings.Contains(body, "Moved 2 of 10 from Main › Hall to Main › Gym: #2, #5.") {
		t.Error("ticked units should be moved, flashed and recorded")
	}
	if hall, _ := st.GetItem(1); hall.Quantity != 8 || hall.PlaceID != 3 {
		t.Errorf("chairs left = %+v", hall)
	}
	if gym, _ := st.GetItem(2); gym.Quantity != 2 || gym.PlaceID != 4 || gym.Name != "Chairs" {
		t.Errorf("chairs moved = %+v", gym)
	}
	// Moving them all moves the whole item.
	c.post("/items/2/move", "/items/2/move", url.Values{"place_id": {"3"}, "moved_on": {"2026-03-02"}, "count": {"2"}}, 200)
	if gym, _ := st.GetItem(2); gym.PlaceID != 3 || gym.Quantity != 2 {
		t.Errorf("whole move = %+v", gym)
	}
	// Those left behind keep their numbers as IDs.
	if body := c.get("/items/1", 200); !strings.Contains(body, `<strong>#2</strong> <span class="badge">ID 3</span>`) {
		t.Error("renumbered units should show the ID they had")
	}
}

func TestUnitIDs(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"username": {"admin"}, "display_name": {"Alex"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	addPlaces(c, "building:Main", "room:2::Hall", "room:2::Gym") // places 2, 3, 4
	mics := url.Values{"place_id": {"3"}, "name": {"Mics"}, "quantity": {"2"}, "portable": {"1"}}

	// IDs given when it's added; they must be one each and no more than there are.
	mics.Set("unit_ids", "Mic 1, mic 1")
	if body := c.post("/items/new", "/items", mics, 422); !strings.Contains(body, "Each one needs its own ID; more than one is mic 1.") {
		t.Error("duplicate IDs should be refused")
	}
	mics.Set("unit_ids", "A, B, C")
	if body := c.post("/items/new", "/items", mics, 422); !strings.Contains(body, "There are 3 IDs listed for 2 of them.") {
		t.Error("too many IDs should be refused")
	}
	mics.Set("unit_ids", "Mic 1, Mic 2")
	c.post("/items/new", "/items", mics, 200)
	if body := c.get("/items/1/move", 200); !strings.Contains(body, "#1 (ID Mic 1)") {
		t.Error("move form should show IDs")
	}

	// Mic 1 goes to the gym; Mic 2 is still Mic 2, now as #1 in the hall.
	c.post("/items/1/move", "/items/1/move", url.Values{"place_id": {"4"}, "moved_on": {"2026-03-01"}, "count": {"2"}, "units": {"1"}}, 200)
	if body := c.get("/items/1", 200); !strings.Contains(body, "<dt>ID</dt><dd>Mic 2</dd>") {
		t.Error("the mic left behind should still be Mic 2")
	}
	if body := c.get("/items/2", 200); !strings.Contains(body, "<dt>ID</dt><dd>Mic 1</dd>") {
		t.Error("the mic that moved should still be Mic 1")
	}
	if !strings.Contains(c.get("/items/2/edit", 200), `name="unit_ids" value="Mic 1"`) {
		t.Error("edit form should list the IDs")
	}

	// IDs are per kind: handheld mics have their own 1.
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Handheld mics"}, "quantity": {"1"}, "portable": {"1"}}, 200)
	if body := c.get("/items/3", 200); !strings.Contains(body, "<dt>ID</dt><dd>1</dd>") {
		t.Error("a single portable item should show its ID")
	}
	// A second set of lapel mics carries on from the first.
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Lapel mics"}, "quantity": {"2"}, "portable": {"1"}}, 200)
	c.post("/items/new", "/items", url.Values{"place_id": {"4"}, "name": {"lapel mics"}, "quantity": {"2"}, "portable": {"1"}}, 200)
	if units, _ := st.ItemUnits(5, 2, mustDate(t, "2026-03-01")); units[0].ID() != "3" || units[1].Name() != "#2 (ID 4)" {
		t.Errorf("second set of lapel mics = %+v", units)
	}
	if body := c.post("/items/new", "/items", url.Values{"place_id": {"4"}, "name": {"Lapel mics"}, "quantity": {"1"}, "portable": {"1"}, "unit_ids": {"4"}}, 422); !strings.Contains(body, "Other Lapel mics already use 4.") {
		t.Error("an ID another of its kind has should be refused")
	}
	// Adding more gives the new ones the next IDs; fewer drops the last.
	c.post("/items/5/edit", "/items/5", url.Values{"place_id": {"4"}, "name": {"lapel mics"}, "quantity": {"3"}, "portable": {"1"}, "unit_ids": {"3, 4"}}, 200)
	if units, _ := st.ItemUnits(5, 3, mustDate(t, "2026-03-01")); units[2].ID() != "5" {
		t.Errorf("added lapel mic = %+v", units)
	}
	c.post("/items/5/edit", "/items/5", url.Values{"place_id": {"4"}, "name": {"lapel mics"}, "quantity": {"1"}, "portable": {"1"}, "unit_ids": {"3, 4, 5"}}, 200)
	if units, _ := st.ItemUnits(5, 1, mustDate(t, "2026-03-01")); units[0].ID() != "3" {
		t.Errorf("lapel mic left = %+v", units)
	}

	// Changed later on the units page; a blank one is given the next free number.
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Stands"}, "quantity": {"3"}}, 200)
	if body := c.post("/items/6/units", "/items/6/units", url.Values{"id_1": {"S1"}, "id_2": {"s1"}}, 200); !strings.Contains(body, "more than one is s1.") {
		t.Error("the same ID twice should be refused")
	}
	c.post("/items/6/units", "/items/6/units", url.Values{"id_1": {"S1"}, "id_3": {"3"}, "label_3": {"By the piano"}}, 200)
	units, _ := st.ItemUnits(6, 3, mustDate(t, "2026-03-01"))
	if units[0].Name() != "#1 (ID S1)" || units[1].Name() != "#2" || units[2].Name() != "#3 – By the piano" {
		t.Errorf("units = %+v", units)
	}
	// Editing the item without touching the IDs leaves them alone.
	c.post("/items/6/edit", "/items/6", url.Values{"place_id": {"3"}, "name": {"Stands"}, "quantity": {"3"}, "unit_ids": {"S1, 2, 3"}}, 200)
	if units, _ := st.ItemUnits(6, 3, mustDate(t, "2026-03-01")); units[0].ID() != "S1" || units[2].Label != "By the piano" {
		t.Errorf("units after edit = %+v", units)
	}
}

// addPlaces adds places through the form, in order. Each is
// "kind:parent:number:name", or "building:Name" for a building on the site.
func addPlaces(c *client, specs ...string) {
	c.t.Helper()
	for _, spec := range specs {
		parts := strings.Split(spec, ":")
		v := url.Values{"kind": {parts[0]}, "name": {parts[len(parts)-1]}}
		if len(parts) == 4 {
			v.Set("parent_id", parts[1])
			v.Set("number", parts[2])
		}
		c.post("/places/new", "/places", v, 200)
	}
}

func mustDate(t *testing.T, d string) time.Time {
	t.Helper()
	v, err := time.Parse(store.DateLayout, d)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSiteAddressAndQRCodes(t *testing.T) {
	c, _ := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"username": {"admin"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	addPlaces(c, "building:Main", "room:2:104:Nursery", "room:2::Gym") // places 2, 3, 4

	// No site address yet: codes use the address in use, with warnings.
	sheet := c.get("/places/3/qr", 200)
	for _, want := range []string{"<svg class=\"qr\"", c.base + "/report?place=3", "104 – Nursery", "Reporting problems with places without signing in is off", "set the site address"} {
		if !strings.Contains(sheet, want) {
			t.Errorf("room QR page missing %q", want)
		}
	}

	settings := url.Values{"site_name": {"Main"}, "due_soon_days": {"30"}, "public_reports": {"1"}}
	for _, bad := range []string{"ftp://x.org", "https://", "https://x.org/?a=1", "https://me@x.org"} {
		settings.Set("site_url", bad)
		if body := c.post("/admin/settings", "/admin/settings", settings, 422); !strings.Contains(body, "Site address should look like") {
			t.Errorf("site address %q should be refused", bad)
		}
	}
	// A bare host means https; a trailing slash is trimmed.
	settings.Set("site_url", "maint.example.org/")
	c.post("/admin/settings", "/admin/settings", settings, 200)
	if body := c.get("/admin/settings", 200); !strings.Contains(body, `value="https://maint.example.org"`) || !strings.Contains(body, "https://maint.example.org/report") {
		t.Error("settings should show the normalized site address and use it for the report link")
	}

	sheet = c.get("/places/2/qr?inside=1", 200)
	for _, want := range []string{"https://maint.example.org/report?place=2", "https://maint.example.org/report?place=3", "https://maint.example.org/report?place=4",
		`<p class="qr-card__sub">Building</p>`, `<p class="qr-card__sub">Main</p>`} {
		if !strings.Contains(sheet, want) {
			t.Errorf("building QR sheet missing %q", want)
		}
	}
	if strings.Count(sheet, "<svg class=\"qr\"") != 3 || strings.Contains(sheet, "Reporting problems with places without signing in is off") || strings.Contains(sheet, "set the site address") {
		t.Error("building sheet should have 3 codes and no warnings")
	}
	// Clearing it goes back to the request's address.
	settings.Set("site_url", "")
	c.post("/admin/settings", "/admin/settings", settings, 200)
	if !strings.Contains(c.get("/places/4/qr", 200), c.base+"/report?place=4") {
		t.Error("blank site address should fall back to the request")
	}
}

func TestQRSVG(t *testing.T) {
	svg, err := qrSVG("https://example.org/report?room=1&x=<y>")
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.HasPrefix(s, "<svg") || !strings.Contains(s, `aria-label="QR code for https://example.org/report?room=1&amp;x=&lt;y&gt;"`) || !strings.Contains(s, "<path d=\"M") {
		t.Errorf("unexpected svg: %.200s", s)
	}
}

func TestPlaces(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"site_name": {"Grace Church"}, "username": {"admin"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	// Site 1 is made for the first building (2).
	addPlaces(c, "building:Main", "floor:2::2nd floor", "area:3::Classrooms", "room:4:201:Nursery", "room:2::Sanctuary", "area:6::Stage")
	if site, _ := st.GetPlace(1); site.Kind != "site" || site.Name != "Grace Church" {
		t.Fatalf("site = %+v", site)
	}
	c.post("/places/4/edit", "/places/4", url.Values{"kind": {"area"}, "parent_id": {"3"}, "name": {"Classrooms"}, "tags": {"classrooms, Kids,  kids "}}, 200)
	if p, _ := st.GetPlace(4); strings.Join(p.Tags, "|") != "classrooms|Kids" {
		t.Errorf("tags = %q", p.Tags)
	}

	// The rules for what goes inside what, and nothing goes inside itself.
	for _, bad := range []struct {
		path string
		form url.Values
		want string
	}{
		{"/places", url.Values{"kind": {"room"}, "parent_id": {"5"}, "name": {"Closet"}}, "A room can&#39;t go inside a room. It can go inside a building, floor or area."},
		{"/places", url.Values{"kind": {"floor"}, "parent_id": {"3"}, "name": {"Mezzanine"}}, "A floor can&#39;t go inside a floor"},
		{"/places", url.Values{"kind": {"building"}, "parent_id": {"5"}, "name": {"Shed"}}, "A building can&#39;t go inside a room"},
		{"/places", url.Values{"kind": {"castle"}, "parent_id": {"2"}, "name": {"X"}}, "Choose what kind of place it is"},
		{"/places/4", url.Values{"kind": {"room"}, "parent_id": {"3"}, "name": {"Classrooms"}}, "201 – Nursery (a room) is inside it, and a room can&#39;t hold a room"},
		{"/places/3", url.Values{"kind": {"floor"}, "parent_id": {"4"}, "name": {"2nd floor"}}, "can&#39;t go inside itself"},
		{"/places/bulk", url.Values{"kind": {"building"}, "parent_id": {"5"}, "lines": {"Shed"}}, "A building can&#39;t go inside a room"},
	} {
		if body := c.post("/places/new", bad.path, bad.form, 422); !strings.Contains(body, bad.want) {
			t.Errorf("%s %v: missing %q", bad.path, bad.form, bad.want)
		}
	}

	// A room inside an area, and an area holding a floor, are fine.
	addPlaces(c, "room:7::Green room", "floor:4::Mezzanine") // places 8, 9

	// A shared stairwell: lives on the 2nd floor, also off the Sanctuary.
	c.post("/places/new", "/places", url.Values{"kind": {"area"}, "parent_id": {"3"}, "name": {"Stairwell"}, "also_in": {"6", "3"}}, 200) // 10
	if p, _ := st.GetPlace(10); !slices.Equal(p.AlsoIn, []int64{6}) {
		t.Errorf("stairwell also in %v", p.AlsoIn)
	}
	if body := c.get("/places/6", 200); !strings.Contains(body, "Stairwell") || !strings.Contains(body, "Shared, lives in Main › 2nd floor › Stairwell") {
		t.Error("the Sanctuary should list the shared stairwell")
	}
	if body := c.get("/places/10", 200); !strings.Contains(body, `Shared: also in <a href="/places/6">Main › Sanctuary</a>`) {
		t.Error("the stairwell should say where else it is")
	}
	if body := c.get("/places/10/edit", 200); !strings.Contains(body, `name="also_in" value="6" checked`) || strings.Contains(body, `name="also_in" value="10"`) {
		t.Error("edit form should tick the places it's also in, and not offer itself")
	}
	addPlaces(c, "area:10::Landing") // 11
	if body := c.post("/places/10/edit", "/places/10", url.Values{"kind": {"area"}, "parent_id": {"3"}, "name": {"Stairwell"}, "also_in": {"11"}}, 422); !strings.Contains(body, "Landing is inside this area") {
		t.Error("a shared area can't also be in something inside it")
	}
	c.post("/report", "/report", url.Values{"place_id": {"11"}, "title": {"Loose handrail"}}, 200)
	if !strings.Contains(c.get("/places/6", 200), "Loose handrail") {
		t.Error("problems in a shared area show in the places it's also in")
	}

	// Things inside a place show up on it.
	c.post("/items/new", "/items", url.Values{"place_id": {"5"}, "name": {"Crib"}}, 200)
	c.post("/report", "/report", url.Values{"place_id": {"5"}, "title": {"Crib wobbles"}}, 200)
	main := c.get("/places/2", 200)
	for _, want := range []string{"Crib wobbles", "Include everything inside (1)", "2nd floor", "Sanctuary"} {
		if !strings.Contains(main, want) {
			t.Errorf("building page missing %q", want)
		}
	}
	if body := c.get("/places/2?all=1", 200); !strings.Contains(body, "Main › 2nd floor › Classrooms › 201 – Nursery") {
		t.Error("everything-inside view should list the crib with where it is")
	}
	index := c.get("/places", 200)
	for _, want := range []string{"Whole site", `href="/places?tag=classrooms"`, "tree__row--d3", "1 problem"} {
		if !strings.Contains(index, want) {
			t.Errorf("places index missing %q", want)
		}
	}

	// Tags gather places and what's in them.
	tag := c.get("/places?tag=KIDS", 200)
	if !strings.Contains(tag, "Classrooms") || !strings.Contains(tag, "Crib wobbles") || !strings.Contains(tag, "1 item in them") {
		t.Error("tag page should list tagged places and their problems")
	}
	c.get("/places?tag=nope", 404)
	if !strings.Contains(c.get("/items?tag=kids", 200), "Crib") || strings.Contains(c.get("/items?tag=classrooms&place=6", 200), "Crib") {
		t.Error("items should filter by tag and place")
	}
	if !strings.Contains(c.get("/problems?tag=kids", 200), "Crib wobbles") {
		t.Error("problems should filter by tag")
	}

	// Moving a place takes everything in it along.
	body := c.post("/places/4/edit", "/places/4", url.Values{"kind": {"area"}, "parent_id": {"2"}, "name": {"Classrooms"}, "tags": {"kids"}}, 200)
	if !strings.Contains(body, "Moved, along with everything in it.") {
		t.Error("moving a place should say so")
	}
	if it, _ := st.GetItem(1); it.Location() != "Main › Classrooms › 201 – Nursery" {
		t.Errorf("crib is at %q", it.Location())
	}
	// Deleting a place moves what was in it up a level.
	body = c.post("/places/4", "/places/4/delete", url.Values{}, 200)
	if !strings.Contains(body, "Anything that was in it is now here.") || !strings.Contains(body, "201 – Nursery") {
		t.Error("deleting a place should land on its parent with its contents")
	}
	if it, _ := st.GetItem(1); it.Location() != "Main › 201 – Nursery" {
		t.Errorf("crib is at %q", it.Location())
	}
	if body := c.post("/places/1", "/places/1/delete", url.Values{}, 200); !strings.Contains(body, "still has things in it") {
		t.Error("a site with things in it can't be deleted")
	}

	// Several areas at once inside a room.
	if body := c.post("/places/bulk?parent=5", "/places/bulk", url.Values{"parent_id": {"5"}, "kind": {"area"}, "lines": {"Closet\nChanging table"}}, 200); !strings.Contains(body, "2 areas added") {
		t.Error("bulk add of areas")
	}

	// With a second site, sites show in paths and new buildings need one.
	c.post("/places/new?kind=site", "/places", url.Values{"kind": {"site"}, "name": {"North campus"}, "address": {"9 North Rd"}}, 200)
	if body := c.post("/places/new", "/places", url.Values{"kind": {"building"}, "name": {"Barn"}}, 422); !strings.Contains(body, "Choose where it is") {
		t.Error("with two sites a building needs a site")
	}
	if it, _ := st.GetItem(1); it.Location() != "Grace Church › Main › 201 – Nursery" {
		t.Errorf("with two sites the crib is at %q", it.Location())
	}
	if !strings.Contains(c.get("/places", 200), "North campus") {
		t.Error("index should list both sites")
	}

	// Links and QR codes from before places still work.
	st.DB.Exec(`UPDATE places SET building_id = 7 WHERE id = 2`)
	st.DB.Exec(`UPDATE places SET room_id = 9 WHERE id = 5`)
	if body := c.get("/rooms/9", 200); !strings.Contains(body, "<h1>201 – Nursery</h1>") {
		t.Error("/rooms/9 should lead to the room it became")
	}
	if body := c.get("/buildings/7/qr", 200); strings.Count(body, `<svg class="qr"`) < 3 {
		t.Error("/buildings/7/qr should print the building's codes")
	}
	if body := c.get("/report?room=9", 200); !strings.Contains(body, `value="5" data-path="Grace Church › Main › 201 – Nursery" data-kind="room" selected>`) {
		t.Error("old room QR codes should preselect the room")
	}
}

func TestProducts(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"username": {"admin"}, "display_name": {"Alex"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	addPlaces(c, "building:Main", "room:2::Hall", "room:2::Gym") // places 2, 3, 4

	// Counted from the start: IDs on the form are ignored.
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Folding chairs"}, "category": {"Furniture"}, "quantity": {"40"},
		"portable": {"1"}, "tracking": {"count"}, "unit_ids": {"A, A"}}, 200)
	// The same name is the same product: its category and counting stand.
	body := c.post("/items/new?product=1", "/items", url.Values{"place_id": {"4"}, "name": {"folding chairs"}, "category": {"Seating"}, "quantity": {"60"},
		"portable": {"1"}, "tracking": {"each"}}, 200)
	if !strings.Contains(body, "100 Folding chairs in all") || !strings.Contains(body, `<span class="badge">Counted</span>`) {
		t.Error("item page should show the product's total and that it's counted")
	}
	if it, _ := st.GetItem(2); it.ProductID != 1 || !it.Counted || it.Category != "Furniture" {
		t.Errorf("gym chairs = %+v", it)
	}
	if body := c.get("/items/new?product=1", 200); !strings.Contains(body, `value="Folding chairs"`) {
		t.Error("adding more of a product should fill in its name")
	}
	if body := c.get("/items/2/move", 200); strings.Contains(body, `name="units"`) {
		t.Error("counted items have no units to pick")
	}
	if body := c.get("/items/2/units", 200); !strings.Contains(body, "only counted") {
		t.Error("counted items have no IDs page")
	}
	body = c.get("/products", 200)
	if !strings.Contains(body, "Folding chairs") || !strings.Contains(body, `<span class="qty">100</span>`) {
		t.Error("products page should total chairs across places")
	}
	if body := c.get("/products?place=4", 200); !strings.Contains(body, `<span class="qty">60</span>`) {
		t.Error("products in one place should total only those")
	}
	if body := c.get("/products/1", 200); !strings.Contains(body, "Main › Hall") || !strings.Contains(body, "Main › Gym") {
		t.Error("product page should list where they are")
	}

	// Editing the product; renaming onto another is refused.
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Chairs, folding"}, "quantity": {"2"}, "portable": {"1"}}, 200)
	if body := c.post("/products/2/edit", "/products/2", url.Values{"name": {"folding chairs"}, "tracking": {"each"}}, 422); !strings.Contains(body, "already a product called folding chairs") {
		t.Error("renaming onto another product should be refused")
	}
	c.post("/products/1/edit", "/products/1", url.Values{"name": {"Folding chairs"}, "category": {"Furniture"}, "tracking": {"count"}, "notes": {"Stack 10 high"}}, 200)
	body = c.post("/products/2/edit", "/products/2/merge", url.Values{"into": {"1"}}, 200)
	if !strings.Contains(body, "Chairs, folding merged into Folding chairs.") || !strings.Contains(body, "Stack 10 high") {
		t.Error("merging should land on the product it went into")
	}
	if p, _ := st.GetProduct(1); p.Total != 102 || p.ItemCount != 3 {
		t.Errorf("after merge = %+v", p)
	}
	if body := c.post("/products/1/edit", "/products/1/merge", url.Values{"into": {"1"}}, 422); !strings.Contains(body, "Choose the product to merge it into.") {
		t.Error("merging into itself should be refused")
	}
	for _, p := range []string{"/products", "/products/1", "/products/1/edit", "/products?q=chair&counted=1&portable=1&category=Furniture"} {
		c.get(p, 200)
	}
	c.get("/products/99", 404)
}

// apiGet calls the API with a token ("" for none) and decodes the JSON.
func (c *client) apiGet(token, path string, wantStatus int, into any) {
	c.t.Helper()
	req, _ := http.NewRequest("GET", c.base+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("GET %s: status %d, want %d\n%s", path, resp.StatusCode, wantStatus, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		c.t.Fatalf("GET %s: content type %q", path, ct)
	}
	if into != nil {
		if err := json.Unmarshal(body, into); err != nil {
			c.t.Fatalf("GET %s: %v\n%s", path, err, body)
		}
	}
}

var tokenRe = regexp.MustCompile(`<code class="token">(mt_[^<]+)</code>`)

func TestAPI(t *testing.T) {
	c, _ := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"username": {"admin"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	addPlaces(c, "building:Main", "room:2::Hall", "room:2::Gym") // places 2, 3, 4
	c.post("/places/3/edit", "/places/3", url.Values{"kind": {"room"}, "parent_id": {"2"}, "name": {"Hall"}, "tags": {"events"}}, 200)
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Folding chairs"}, "category": {"Furniture"}, "quantity": {"40"}, "portable": {"1"}, "tracking": {"count"}}, 200)
	c.post("/items/new", "/items", url.Values{"place_id": {"4"}, "name": {"Folding chairs"}, "quantity": {"60"}, "portable": {"1"}}, 200)
	c.post("/items/new", "/items", url.Values{"place_id": {"4"}, "name": {"Lapel mics"}, "quantity": {"2"}, "portable": {"1"}, "unit_ids": {"Mic A, Mic B"}}, 200)
	c.post("/supplies/new", "/supplies", url.Values{"place_id": {"3"}, "name": {"Tablecloths"}, "reusable": {"1"}, "quantity": {"8"}, "in_use": {"2"}}, 200)

	// No token, a made-up one, or only a session: refused.
	var e struct{ Error string }
	c.apiGet("", "/api/v1/products", 401, &e)
	if !strings.Contains(e.Error, "Bearer") {
		t.Errorf("error = %q", e.Error)
	}
	c.apiGet("mt_made-up", "/api/v1/products", 401, nil)

	body := c.post("/admin/tokens", "/admin/tokens", url.Values{"name": {"Event planner"}}, 200)
	m := tokenRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("the new token should be shown once")
	}
	token := m[1]
	if body := c.get("/admin/tokens", 200); strings.Contains(body, token) || !strings.Contains(body, token[:9]+"…") || !strings.Contains(body, "Never") {
		t.Error("the token list should show only the start of each token")
	}
	if body := c.post("/admin/tokens", "/admin/tokens", url.Values{"name": {""}}, 422); !strings.Contains(body, "Name what will use it") {
		t.Error("a token needs a name")
	}

	var products struct{ Products []apiProductJSON }
	c.apiGet(token, "/api/v1/products", 200, &products)
	if len(products.Products) != 2 || products.Products[0].Name != "Folding chairs" || products.Products[0].Total != 100 || !products.Products[0].Counted {
		t.Errorf("products = %+v", products)
	}
	c.apiGet(token, "/api/v1/products?tag=events&portable=1", 200, &products)
	if len(products.Products) != 1 || products.Products[0].Total != 40 {
		t.Errorf("products at events places = %+v", products)
	}
	c.apiGet(token, "/api/v1/products?place=99", 400, &e)
	if e.Error != "place 99 doesn't exist" {
		t.Errorf("error = %q", e.Error)
	}

	var product struct {
		apiProductJSON
		Items []apiItemJSON
	}
	c.apiGet(token, "/api/v1/products/1", 200, &product)
	if product.Total != 100 || len(product.Items) != 2 || product.Items[0].Place.Path != "Main › Gym" || product.Items[1].Quantity != 40 {
		t.Errorf("product = %+v", product)
	}
	c.apiGet(token, "/api/v1/products/99", 404, nil)

	var items struct{ Items []apiItemJSON }
	c.apiGet(token, "/api/v1/items?place=4&q=mic", 200, &items)
	if len(items.Items) != 1 || items.Items[0].Name != "Lapel mics" || items.Items[0].Units != nil {
		t.Errorf("items = %+v", items)
	}
	var item apiItemJSON
	c.apiGet(token, "/api/v1/items/3", 200, &item)
	if len(item.Units) != 2 || item.Units[1].ID != "Mic B" {
		t.Errorf("mics = %+v", item)
	}
	var chairs apiItemJSON
	c.apiGet(token, "/api/v1/items/1", 200, &chairs)
	if !chairs.Counted || chairs.Units != nil {
		t.Errorf("counted chairs = %+v", chairs)
	}

	var supplies struct{ Supplies []apiSupplyJSON }
	c.apiGet(token, "/api/v1/supplies?q=table", 200, &supplies)
	if len(supplies.Supplies) != 1 || supplies.Supplies[0].OnHand != 8 || supplies.Supplies[0].InUse != 2 || supplies.Supplies[0].Total != 10 {
		t.Errorf("supplies = %+v", supplies)
	}
	var supply apiSupplyJSON
	c.apiGet(token, "/api/v1/supplies/1", 200, &supply)
	if supply.Name != "Tablecloths" || !supply.Reusable || supply.Stock != "ok" {
		t.Errorf("supply = %+v", supply)
	}

	var places struct{ Places []apiPlaceJSON }
	c.apiGet(token, "/api/v1/places", 200, &places)
	if len(places.Places) != 4 || places.Places[0].ParentID != nil || *places.Places[2].ParentID != 2 {
		t.Errorf("places = %+v", places)
	}
	c.apiGet(token, "/api/v1/places?tag=events", 200, &places)
	if len(places.Places) != 1 || places.Places[0].Name != "Hall" || places.Places[0].Tags[0] != "events" {
		t.Errorf("events places = %+v", places)
	}
	var place struct {
		apiPlaceJSON
		Children []int64
		Items    []apiItemJSON
		Supplies []apiSupplyJSON
	}
	c.apiGet(token, "/api/v1/places/2", 200, &place)
	if len(place.Children) != 2 || len(place.Items) != 3 || len(place.Supplies) != 1 {
		t.Errorf("place = %+v", place)
	}
	c.apiGet(token, "/api/v1/places/2?direct=1", 200, &place)
	if len(place.Items) != 0 || len(place.Supplies) != 0 {
		t.Errorf("only in the building itself = %+v", place)
	}
	c.apiGet(token, "/api/v1/nope", 404, nil)
	if body := c.get("/admin/tokens", 200); strings.Contains(body, "Never") {
		t.Error("using a token should record when")
	}

	// Revoked: refused from then on.
	c.post("/admin/tokens", "/admin/tokens/1/delete", url.Values{}, 200)
	c.apiGet(token, "/api/v1/products", 401, nil)
}

func TestQRStickersAndScanning(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{"username": {"admin"}, "display_name": {"Alex"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	addPlaces(c, "building:Main", "room:2::Hall", "room:2::Gym") // places 2, 3, 4
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Lapel mics"}, "quantity": {"3"}, "portable": {"1"}, "unit_ids": {"Mic A, Mic B, Mic C"}}, 200)
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Folding chairs"}, "quantity": {"40"}, "portable": {"1"}, "tracking": {"count"}}, 200)
	c.post("/items/new", "/items", url.Values{"place_id": {"4"}, "name": {"Furnace"}}, 200)
	c.post("/supplies/new", "/supplies", url.Values{"place_id": {"3"}, "name": {"Paper towels"}, "quantity": {"12"}, "reorder_at": {"2"}}, 200)

	// A sticker per unit, carrying its ID; one for a counted or fixed item.
	body := c.get("/items/1/qr", 200)
	if strings.Count(body, `<svg class="qr"`) != 3 || !strings.Contains(body, c.base+"/report?item=1&amp;unit=Mic&#43;B") || !strings.Contains(body, "qr-sheet--small") {
		t.Error("each mic should get a sticker with its ID")
	}
	if body := c.get("/items/2/qr", 200); strings.Count(body, `<svg class="qr"`) != 1 || !strings.Contains(body, c.base+"/report?item=2") {
		t.Error("counted chairs get one code for the lot")
	}
	if body := c.get("/items/3/qr", 200); strings.Count(body, `<svg class="qr"`) != 1 || !strings.Contains(body, "Main › Gym") {
		t.Error("a fixed item gets one code saying where it is")
	}
	if body := c.get("/products/1/qr", 200); strings.Count(body, `<svg class="qr"`) != 3 || !strings.Contains(body, "Reporting problems with items without signing in is off") {
		t.Error("a product's sheet covers every one, and warns that visitors must sign in")
	}

	// Scanning a unit's code: the report is about that one.
	body = c.get("/report?item=1&unit=Mic+B", 200)
	if !strings.Contains(body, `<input type="hidden" name="unit" value="2">`) || !strings.Contains(body, `<span class="badge">ID Mic B</span>`) {
		t.Error("the report form should be about Mic B")
	}
	c.post("/report?item=1&unit=Mic+B", "/report", url.Values{"fixed": {"1"}, "place_id": {"3"}, "item_id": {"1"}, "unit": {"2"}, "title": {"Crackles"}}, 200)
	if p, _ := st.GetProblem(1); p.ItemID != 1 || p.Unit != 2 || p.UnitTag != "Mic B" {
		t.Errorf("problem = %+v", p)
	}
	// After Mic B moves to the gym, its sticker still finds it.
	c.post("/items/1/move", "/items/1/move", url.Values{"place_id": {"4"}, "moved_on": {"2026-03-01"}, "units": {"2"}}, 200)
	if body := c.get("/report?item=1&unit=Mic+B", 200); !strings.Contains(body, `name="item_id" value="4"`) || !strings.Contains(body, "Main › Gym") {
		t.Error("Mic B's sticker should follow it to the gym")
	}
	// A counted item has no units to name.
	if body := c.get("/report?item=2&unit=1", 200); strings.Contains(body, `name="unit"`) {
		t.Error("counted items have no unit to report")
	}

	// Places and items are opened to visitors separately.
	c.post("/admin/settings", "/admin/settings", url.Values{"site_name": {"Main"}, "due_soon_days": {"30"}, "public_reports": {"1"}}, 200)
	c.post("/", "/logout", url.Values{}, 200)
	if body := c.get("/report?item=1&unit=Mic+A", 200); !strings.Contains(body, "Sign in") || strings.Contains(body, "Your name") {
		t.Error("item reports are off, so an item's code asks visitors to sign in")
	}
	if body := c.get("/report?place=3", 200); !strings.Contains(body, "Your name") || strings.Contains(body, `name="item_id"`) {
		t.Error("place reports are on, without items to pick")
	}
	c.post("/report?place=3", "/report", url.Values{"place_id": {"3"}, "item_id": {"1"}, "title": {"X"}, "reporter_name": {"Pat"}}, 200)
	if _, err := st.GetProblem(2); err == nil {
		t.Error("a visitor shouldn't be able to report an item while item reports are off")
	}

	c.post("/login", "/login", url.Values{"username": {"admin"}, "password": {"a long password"}}, 200)
	c.post("/admin/settings", "/admin/settings", url.Values{"site_name": {"Main"}, "due_soon_days": {"30"}, "public_item_reports": {"1"}}, 200)
	c.post("/", "/logout", url.Values{}, 200)
	if body := c.get("/report?place=3", 200); !strings.Contains(body, "Sign in") || strings.Contains(body, "Your name") {
		t.Error("place reports are off now")
	}
	body = c.get("/report?item=1&unit=Mic+A", 200)
	if !strings.Contains(body, "Your name") || !strings.Contains(body, `<span class="badge">ID Mic A</span>`) || strings.Contains(body, "Something else here?") {
		t.Error("item reports are on: visitors get the form for Mic A, and nothing else")
	}
	body = c.post("/report?item=1&unit=Mic+A", "/report", url.Values{"fixed": {"1"}, "place_id": {"3"}, "item_id": {"1"}, "unit": {"1"}, "title": {"Dead battery"}, "reporter_name": {"Pat"}}, 200)
	if !strings.Contains(body, "Thanks!") || !strings.Contains(body, "<strong>Lapel mics</strong>") {
		t.Error("visitors should be thanked, back on the item's form")
	}
	if p, _ := st.GetProblem(2); p.ReporterName != "Pat" || p.Unit != 1 {
		t.Errorf("visitor's item report = %+v", p)
	}
	if body := c.get("/scan", 200); !strings.Contains(body, "Sign in") {
		t.Error("scanning is for signed-in users")
	}

	// Supplies: a code to scan, and asking for more.
	c.post("/login", "/login", url.Values{"username": {"admin"}, "password": {"a long password"}}, 200)
	if body := c.get("/scan", 200); !strings.Contains(body, `data-controller="scan"`) {
		t.Error("the scan page should load the scanner")
	}
	if body := c.get("/supplies/1/qr", 200); !strings.Contains(body, c.base+"/supplies/1") || !strings.Contains(body, "Scan to update the count or ask for more") {
		t.Error("a supply's code should open its page")
	}
	if body := c.get("/supplies/qr?place=3", 200); strings.Count(body, `<svg class="qr"`) != 1 {
		t.Error("the supplies sheet follows the filter")
	}
	body = c.post("/supplies/1", "/supplies/1/request", url.Values{"note": {"Before Sunday"}}, 200)
	if !strings.Contains(body, "Asked for more Paper towels") || !strings.Contains(body, "Alex asked") || !strings.Contains(body, "Before Sunday") {
		t.Error("asking for more should show who asked and why")
	}
	if body := c.get("/", 200); !strings.Contains(body, "Paper towels") || !strings.Contains(body, "More wanted") {
		t.Error("the dashboard should list supplies people asked for")
	}
	c.post("/supplies/1", "/supplies/1/adjust", url.Values{"kind": {"restocked"}, "amount": {"6"}}, 200)
	if sp, _ := st.GetSupply(1); sp.Requested() || sp.Quantity != 18 {
		t.Errorf("restocking should clear the request: %+v", sp)
	}
	c.post("/supplies/1", "/supplies/1/request", url.Values{}, 200)
	c.post("/supplies/1", "/supplies/1/request/cancel", url.Values{}, 200)
	if sp, _ := st.GetSupply(1); sp.Requested() {
		t.Error("the request should be cleared")
	}
}
