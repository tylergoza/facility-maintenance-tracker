package server

import (
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
