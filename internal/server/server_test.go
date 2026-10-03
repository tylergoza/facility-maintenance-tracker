package server

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
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

	c.post("/buildings/new", "/buildings", url.Values{"name": {"Sanctuary"}, "address": {"1 Main St"}}, 200)
	c.post("/rooms/new?building=1", "/rooms", url.Values{"building_id": {"1"}, "name": {"Nursery"}, "floor": {"1st"}}, 200)
	c.post("/items/new", "/items", url.Values{"building_id": {"1"}, "room_id": {"1"}, "name": {"Smoke detector"}, "category": {"Safety"}}, 200)
	c.post("/items/new", "/items", url.Values{"building_id": {"1"}, "name": {"Roof"}}, 200)
	c.post("/tasks/new?item=1", "/tasks", url.Values{
		"item_id": {"1"}, "name": {"Test alarm"}, "recurring": {"1"}, "interval_value": {"6"}, "interval_unit": {"months"},
		"last_completed_on": {"2020-01-01"},
	}, 200)
	c.post("/tasks/new?item=2", "/tasks", url.Values{"item_id": {"2"}, "name": {"Inspect shingles"}}, 200)

	task, err := st.GetTask(1)
	if err != nil || task.NextDueOn != "2020-07-01" {
		t.Fatalf("next due should be computed from last done: %+v %v", task, err)
	}

	// Room must belong to the chosen building.
	c.post("/buildings/new", "/buildings", url.Values{"name": {"Parsonage"}}, 200)
	if body := c.post("/items/new", "/items", url.Values{"building_id": {"2"}, "room_id": {"1"}, "name": {"X"}}, 422); !strings.Contains(body, "not in the selected building") {
		t.Fatal("expected room/building validation error")
	}

	// Every signed-in page renders.
	for _, p := range []string{
		"/", "/?building=1", "/buildings", "/buildings/1", "/buildings/1/edit", "/rooms/new", "/rooms/bulk?building=1", "/rooms/1", "/rooms/1/edit",
		"/items", "/items?q=smoke&building=1", "/items/new", "/items/1", "/items/1/edit", "/items/1/log",
		"/tasks/new?item=1", "/tasks/1/edit", "/tasks/1/complete", "/history", "/account",
		"/admin/users", "/admin/users/new", "/admin/users/1/edit", "/admin/settings", "/offline",
	} {
		c.get(p, 200)
	}

	// Room numbers are shown and the dashboard orders by them, with
	// building-wide items (no room) last.
	c.post("/rooms/new?building=1", "/rooms", url.Values{"building_id": {"1"}, "number": {"9"}, "name": {"Office"}}, 200)
	if !strings.Contains(c.get("/rooms/2", 200), "9 – Office") {
		t.Error("room page should show the room number")
	}
	c.post("/items/new", "/items", url.Values{"building_id": {"1"}, "room_id": {"2"}, "name": {"Thermostat"}}, 200)
	c.post("/tasks/new?item=3", "/tasks", url.Values{"item_id": {"3"}, "name": {"Replace batteries"}}, 200)
	if d := c.get("/", 200); strings.Index(d, "Replace batteries") > strings.Index(d, "Inspect shingles") {
		t.Error("dashboard should list numbered rooms before building-wide items")
	}

	// Bulk add rooms: all-or-nothing, with per-line errors.
	if body := c.post("/rooms/bulk?building=1", "/rooms/bulk", url.Values{"building_id": {"1"}, "rooms": {"201, Library\n202,"}}, 422); !strings.Contains(body, "Line 2") {
		t.Error("expected per-line error for missing room name")
	}
	if rooms, _ := st.ListRooms(1); len(rooms) != 2 {
		t.Fatalf("failed bulk add should not save any rooms, got %d", len(rooms))
	}
	body := c.post("/rooms/bulk?building=1", "/rooms/bulk", url.Values{
		"building_id": {"1"}, "floor": {"2nd"}, "rooms": {"201, Library\r\n\r\n202\tChoir Room\nAttic\n"},
	}, 200)
	for _, want := range []string{"3 rooms added", "201 – Library", "202 – Choir Room", "Attic"} {
		if !strings.Contains(body, want) {
			t.Errorf("building page after bulk add missing %q", want)
		}
	}
	if r, _ := st.GetRoom(4); r.Floor != "2nd" || r.Number != "202" || r.Name != "Choir Room" {
		t.Errorf("bulk room not saved correctly: %+v", r)
	}

	// Supplies: add one to a room, use and restock from the room page.
	c.post("/supplies/new?room=1", "/supplies", url.Values{
		"building_id": {"1"}, "room_id": {"1"}, "name": {"Diapers"}, "unit": {"packs"}, "quantity": {"2"}, "reorder_at": {"1"},
	}, 200)
	for _, p := range []string{"/supplies", "/supplies?low=1&building=1&q=diap", "/supplies/new", "/supplies/1", "/supplies/1/edit"} {
		c.get(p, 200)
	}
	if body := c.get("/rooms/1", 200); !strings.Contains(body, "Diapers") || !strings.Contains(body, "2 packs") {
		t.Error("room page should list its supplies")
	}
	body = c.post("/rooms/1", "/supplies/1/adjust", url.Values{"kind": {"used"}, "amount": {"1"}, "next": {"/rooms/1"}}, 200)
	if !strings.Contains(body, "1 packs on hand. Time to reorder.") {
		t.Error("using a supply should flash the new count and low warning")
	}
	if !strings.Contains(c.get("/", 200), "Supplies to restock") {
		t.Error("dashboard should list low supplies")
	}
	if body := c.post("/rooms/1", "/supplies/1/adjust", url.Values{"kind": {"used"}, "amount": {"5"}, "next": {"/rooms/1"}}, 200); !strings.Contains(body, "has only 1 on hand") {
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
		"building_id": {"1"}, "room_id": {"1"}, "name": {"Diapers (size 3)"}, "quantity": {"99"},
	}, 200)
	if sp, _ := st.GetSupply(1); sp.Quantity != 0 || sp.Name != "Diapers (size 3)" {
		t.Errorf("edit changed supply unexpectedly: %+v", sp)
	}

	// Reusable supplies: swap from the room page, then get them back.
	c.post("/supplies/new", "/supplies", url.Values{
		"building_id": {"1"}, "room_id": {"1"}, "name": {"Mop heads"}, "reusable": {"1"},
		"quantity": {"3"}, "in_use": {"1"}, "cleaning": {"0"},
	}, 200)
	body = c.post("/rooms/1", "/supplies/2/adjust", url.Values{"kind": {"swapped"}, "amount": {"1"}, "next": {"/rooms/1"}}, 200)
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
	c.post("/supplies/new", "/supplies", url.Values{"building_id": {"1"}, "name": {"Filters 20x25x1"}, "quantity": {"1"}}, 200)
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

	if body := c.get("/supplies/3", 200); !strings.Contains(body, "Used by") || !strings.Contains(body, "Replace filter (Roof)") {
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
	if body := c.get("/buildings", 200); !strings.Contains(body, "Sign in") {
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
	c.post("/buildings/new", "/buildings", url.Values{"name": {"Main"}}, 200)
	c.post("/rooms/new?building=1", "/rooms", url.Values{"building_id": {"1"}, "number": {"104"}, "name": {"Nursery"}}, 200)
	c.post("/rooms/new?building=1", "/rooms", url.Values{"building_id": {"1"}, "name": {"Gym"}}, 200)
	c.post("/supplies/new", "/supplies", url.Values{"building_id": {"1"}, "name": {"LED A19"}, "unit": {"bulbs"}, "quantity": {"3"}, "reorder_at": {"1"}}, 200)
	c.post("/supplies/new", "/supplies", url.Values{"building_id": {"1"}, "name": {"Mop heads"}, "reusable": {"1"}, "quantity": {"2"}}, 200)

	item := func(name, category, quantity string, extra url.Values) url.Values {
		v := url.Values{"building_id": {"1"}, "room_id": {"1"}, "name": {name}, "category": {category}, "quantity": {quantity}, "supply_source": {"none"}}
		for k, vs := range extra {
			v[k] = vs
		}
		return v
	}

	// Plain counts: 10 outlets, 3 switches.
	c.post("/items/new?room=1", "/items", item("Electrical outlets", "Electrical", "10", nil), 200)
	c.post("/items/new?room=1", "/items", item("Light switches", "Electrical", "3", nil), 200)
	if it, _ := st.GetItem(2); it.Quantity != 3 || it.SupplyID != 0 {
		t.Errorf("switches = %+v", it)
	}
	// 15 lights using a bulb from supplies.
	c.post("/items/new?room=1", "/items", item("Lights", "Lighting", "15", url.Values{"supply_source": {"existing"}, "supply_id": {"1"}, "supply_per": {"1"}}), 200)
	if body := c.post("/items/new", "/items", item("Lights", "", "1", url.Values{"supply_source": {"existing"}, "supply_id": {"2"}, "supply_per": {"1"}}), 422); !strings.Contains(body, "reusable") {
		t.Error("expected reusable supply to be refused")
	}
	// 2 air returns using a new filter supply, added to the building.
	c.post("/items/new?room=1", "/items", item("Air returns", "HVAC", "2", url.Values{"supply_source": {"new"}, "new_supply_name": {"Air filters 12x20x1"},
		"new_supply_unit": {"filters"}, "new_supply_quantity": {"1"}, "supply_per": {"1"}}), 200)
	if sp, err := st.GetSupply(3); err != nil || sp.Name != "Air filters 12x20x1" || sp.Unit != "filters" || sp.Quantity != 1 || sp.BuildingID != 1 || sp.RoomID != 0 {
		t.Fatalf("new filter supply = %+v, %v", sp, err)
	}
	if it, _ := st.GetItem(4); it.SupplyID != 3 || it.SupplyTotal() != 2 {
		t.Errorf("air returns = %+v", it)
	}
	// A portable projector, and a blank count meaning 1.
	c.post("/items/new?room=1", "/items", item("Projector", "Audio/Visual", "", url.Values{"portable": {"1"}}), 200)
	if it, _ := st.GetItem(5); it.Quantity != 1 || !it.Portable {
		t.Errorf("projector = %+v", it)
	}
	if body := c.post("/items/new", "/items", url.Values{"building_id": {"1"}, "quantity": {"0"}, "supply_source": {"new"}}, 422); !strings.Contains(body, "Name is required") ||
		!strings.Contains(body, "How many must be 1") || !strings.Contains(body, "name for the new supply") {
		t.Error("expected name, count and supply validation errors")
	}

	// The room lists everything grouped by category with quick actions.
	room := c.get("/rooms/1", 200)
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

	for _, p := range []string{"/items/new", "/items/3", "/items/3/edit", "/items/3/replace", "/items/5/move", "/items", "/buildings/1"} {
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
	body := c.post("/items/3/replace", "/items/3/replace", url.Values{"performed_on": {"2026-01-10"}, "replaced": {"2"}, "took_supply": {"1"}, "next": {"/rooms/1"}}, 200)
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
	body = c.post("/items/5/move", "/items/5/move", url.Values{"building_id": {"1"}, "room_id": {"2"}, "moved_on": {"2026-03-01"}, "note": {"Youth night"}, "next": {"/items/5"}}, 200)
	if !strings.Contains(body, "Projector moved to Main › Gym.") || !strings.Contains(body, "Moved from Main › 104 – Nursery to Main › Gym.") || !strings.Contains(body, "Youth night") {
		t.Error("move should flash and appear in history")
	}
	if body := c.post("/items/5/move", "/items/5/move", url.Values{"building_id": {"1"}, "room_id": {"2"}, "moved_on": {"2026-03-02"}}, 422); !strings.Contains(body, "where it is now") {
		t.Error("moving to the same place should be refused")
	}
	// Changing the room on the edit form is recorded as a move too.
	c.post("/items/5/edit", "/items/5", item("Projector", "Audio/Visual", "1", url.Values{"room_id": {"1"}, "portable": {"1"}}), 200)
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
	c.post("/buildings/new", "/buildings", url.Values{"name": {"Main"}}, 200)
	c.post("/rooms/new?building=1", "/rooms", url.Values{"building_id": {"1"}, "number": {"110"}, "name": {"Kitchen"}}, 200)
	c.post("/items/new", "/items", url.Values{"building_id": {"1"}, "room_id": {"1"}, "name": {"Sink"}}, 200)

	if !strings.Contains(c.get("/report?item=1", 200), `value="1" data-building-id="1" data-room-id="1" selected>Sink`) {
		t.Error("report form should be preset from the item")
	}
	body := c.post("/report", "/report", url.Values{"building_id": {"1"}, "room_id": {"1"}, "item_id": {"1"}, "title": {"Faucet dripping"}, "details": {"Hot side"}}, 200)
	for _, want := range []string{"Problem reported.", "Faucet dripping", "Nobody is on it yet", "by Alex", "Sink"} {
		if !strings.Contains(body, want) {
			t.Errorf("problem page missing %q", want)
		}
	}
	for _, p := range []string{"/", "/rooms/1", "/items/1", "/buildings/1", "/problems"} {
		if !strings.Contains(c.get(p, 200), "Faucet dripping") {
			t.Errorf("%s should list the open problem", p)
		}
	}
	for _, p := range []string{"/problems/1/edit", "/problems?status=all&building=1&mine=1", "/report?room=1", "/admin/settings"} {
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
	c.post("/problems/1/edit", "/problems/1", url.Values{"building_id": {"1"}, "title": {"Kitchen faucet dripping"}, "reporter_name": {"Alex"}}, 200)
	if p, _ := st.GetProblem(1); p.Title != "Kitchen faucet dripping" || p.RoomID != 0 || p.ItemID != 0 {
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
	c.post("/admin/settings", "/admin/settings", url.Values{"site_name": {"Main"}, "due_soon_days": {"30"}, "public_reports": {"1"}}, 200)
	if !strings.Contains(c.get("/admin/settings", 200), `name="public_reports" value="1" checked`) {
		t.Error("settings should show public reports turned on")
	}
	c.post("/", "/logout", url.Values{}, 200)

	body = c.get("/report?room=1", 200)
	if !strings.Contains(body, "Your name") || !strings.Contains(body, `href="/report"`) {
		t.Error("visitors should get the report form and a nav link to it")
	}
	if body := c.post("/report", "/report", url.Values{"building_id": {"1"}, "title": {"Light out"}}, 422); !strings.Contains(body, "Enter your name") {
		t.Error("visitors must give their name")
	}
	report := url.Values{"building_id": {"1"}, "room_id": {"1"}, "title": {"Light out"}, "reporter_name": {"Pat"}, "reporter_contact": {"555-1234"}, "item_id": {"1"}}
	if body := c.post("/report", "/report", report, 200); !strings.Contains(body, "Thanks!") {
		t.Error("visitor report should thank them")
	}
	if p, _ := st.GetProblem(2); p.ReporterName != "Pat" || p.ReporterContact != "555-1234" || p.ReportedBy != 0 || p.ItemID != 1 || p.RoomID != 1 {
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
	c.post("/buildings/new", "/buildings", url.Values{"name": {"Main"}}, 200)
	c.post("/rooms/new?building=1", "/rooms", url.Values{"building_id": {"1"}, "name": {"Hall"}}, 200)
	c.post("/supplies/new", "/supplies", url.Values{"building_id": {"1"}, "name": {"LED A19"}, "unit": {"bulbs"}, "quantity": {"10"}}, 200)
	c.post("/items/new", "/items", url.Values{"building_id": {"1"}, "room_id": {"1"}, "name": {"Lights"}, "quantity": {"4"},
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
	c.post("/report", "/report", url.Values{"building_id": {"1"}, "room_id": {"1"}, "item_id": {"1"}, "title": {"Light out"}}, 200)
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
	c.post("/report", "/report", url.Values{"building_id": {"1"}, "item_id": {"1"}, "title": {"Buzzing"}}, 200)
	c.post("/items/1/log?problem=2", "/items/1/log", url.Values{"performed_on": {"2026-05-05"}, "notes": {"Replaced dimmer"}, "problem": {"2"}}, 200)
	if p, _ := st.GetProblem(2); p.Status != store.ProblemResolved {
		t.Errorf("problem 2 should be resolved: %+v", p)
	}

	// Fewer units: #3 and #4 drop out of pickers, their history stays.
	c.post("/items/1/edit", "/items/1", url.Values{"building_id": {"1"}, "room_id": {"1"}, "name": {"Lights"}, "quantity": {"2"},
		"supply_source": {"existing"}, "supply_id": {"1"}, "supply_per": {"1"}}, 200)
	if strings.Contains(c.get("/items/1/replace", 200), `name="units" value="3"`) {
		t.Error("units beyond the count should not be offered")
	}
	if !strings.Contains(c.get("/items/1", 200), "· #1, #4") {
		t.Error("history should keep units no longer counted")
	}
	// Single items have no units.
	c.post("/items/new", "/items", url.Values{"building_id": {"1"}, "name": {"Furnace"}}, 200)
	if strings.Contains(c.get("/items/2/log", 200), "Which ones") {
		t.Error("single items should not offer a unit picker")
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
