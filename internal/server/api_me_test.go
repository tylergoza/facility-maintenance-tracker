package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
	"github.com/tylergoza/facility-maintenance-tracker/internal/umtoken"
)

// fakeTokenUM answers User Management's GET /api/v1/token for the tokens
// in its map; anything else (another audience, expired) is 401.
type fakeTokenUM struct {
	srv  *httptest.Server
	hits atomic.Int32
	mu   sync.Mutex
	ok   map[string]umtoken.Answer
	down bool
}

func newFakeTokenUM(t *testing.T) *fakeTokenUM {
	f := &fakeTokenUM{ok: map[string]umtoken.Answer{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/token" {
			http.NotFound(w, r)
			return
		}
		f.hits.Add(1)
		f.mu.Lock()
		ans, ok := f.ok[r.Header.Get("X-Token")]
		down := f.down
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if id, sec, good := r.BasicAuth(); !good || id != "tracker" || sec != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		switch {
		case down:
			w.WriteHeader(http.StatusBadGateway)
		case !ok:
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_token"}`))
		default:
			json.NewEncoder(w).Encode(ans)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTokenUM) give(token, sub, username, role string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ok[token] = umtoken.Answer{User: umtoken.User{Sub: sub, Username: username, Name: strings.ToUpper(username[:1]) + username[1:], Role: role}, Via: "mcp"}
}

func (f *fakeTokenUM) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

// meFixture fills a store through the web pages (local login, no SSO)
// and returns it with an mt_ API token, an editor's web client and the
// database path.
func meFixture(t *testing.T) (*store.Store, string, *client) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := serve(t, st, Config{})
	c.post("/setup", "/setup", url.Values{"username": {"admin"}, "password": {"a long password"}, "password_confirm": {"a long password"}}, 200)
	addPlaces(c, "building:Main", "room:2::Fellowship Hall", "room:2:104:Kitchen") // places 2, 3, 4
	c.post("/places/3/edit", "/places/3", url.Values{"kind": {"room"}, "parent_id": {"2"}, "name": {"Fellowship Hall"}, "tags": {"events"}}, 200)
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Epson projector"}, "category": {"AV"},
		"manufacturer": {"Epson"}, "model": {"EB-992F"}, "serial_number": {"SN-SECRET-1"}, "notes": {"key in the office"}, "portable": {"1"}}, 200)
	c.post("/items/new", "/items", url.Values{"place_id": {"4"}, "name": {"Folding chairs"}, "quantity": {"40"}, "tracking": {"count"}}, 200)
	c.post("/supplies/new", "/supplies", url.Values{"place_id": {"4"}, "name": {"Paper towels"}, "unit": {"rolls"}, "quantity": {"2"}, "reorder_at": {"3"}}, 200)
	c.post("/supplies/new", "/supplies", url.Values{"place_id": {"4"}, "name": {"Dish soap"}, "quantity": {"9"}, "reorder_at": {"2"}, "notes": {"under the sink"}}, 200)
	m := tokenRe.FindStringSubmatch(c.post("/admin/tokens", "/admin/tokens", url.Values{"name": {"Event planner"}}, 200))
	if m == nil {
		t.Fatal("no API token")
	}
	if _, err := st.CreateUser("eddie", "Eddie", "a long password", false); err != nil {
		t.Fatal(err)
	}
	ed := serve(t, st, Config{})
	ed.post("/login", "/login", url.Values{"username": {"eddie"}, "password": {"a long password"}}, 200)
	return st, m[1], ed
}

func serve(t *testing.T, st *store.Store, cfg Config) *client {
	t.Helper()
	srv, err := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}
}

// meServer runs the tracker on st with single sign-on pointing at f (nil
// for none).
func meServer(t *testing.T, st *store.Store, f *fakeTokenUM) (*Server, *client) {
	t.Helper()
	cfg := Config{}
	if f != nil {
		cfg.Env = map[string]string{"SSO_URL": f.srv.URL, "SSO_CLIENT_ID": "tracker", "SSO_CLIENT_SECRET": "s3cret"}
	}
	srv, err := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, &client{t: t, base: ts.URL, http: &http.Client{}}
}

// meGet fetches a /me path and checks the headers every /me answer has.
func (c *client) meGet(path, token string, want int) string {
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
	if resp.StatusCode != want {
		c.t.Fatalf("GET %s: status %d, want %d\n%s", path, resp.StatusCode, want, body)
	}
	h := resp.Header
	if h.Get("Set-Cookie") != "" {
		c.t.Errorf("GET %s set a cookie", path)
	}
	if h.Get("Cache-Control") != "private, no-store" {
		c.t.Errorf("GET %s: Cache-Control %q", path, h.Get("Cache-Control"))
	}
	if h.Get("Access-Control-Allow-Origin") != "" {
		c.t.Errorf("GET %s: CORS header on /me", path)
	}
	if want == 503 && h.Get("Retry-After") == "" {
		c.t.Errorf("GET %s: 503 without Retry-After", path)
	}
	return string(body)
}

func isJSONError(body string) bool {
	var e struct{ Error string }
	return json.Unmarshal([]byte(body), &e) == nil && e.Error != ""
}

var mePaths = []string{"/api/v1/me/places", "/api/v1/me/products", "/api/v1/me/items", "/api/v1/me/supplies"}

func TestMeOffWithoutUserManagement(t *testing.T) {
	st, apiTok, _ := meFixture(t)
	_, c := meServer(t, st, nil)
	for _, p := range append(mePaths, "/api/v1/me/nope") {
		if body := c.meGet(p, "umd_whatever", 404); isJSONError(body) {
			t.Errorf("%s: off should be a plain 404, got %s", p, body)
		}
	}
	// The app-token API is unchanged.
	c.apiGet(apiTok, "/api/v1/items", 200, nil)
	var e struct{ Error string }
	c.apiGet(apiTok, "/api/v1/nope", 404, &e)
	if e.Error == "" {
		t.Error("the /api/ catch-all should still answer JSON")
	}
}

func TestMeTokenChecks(t *testing.T) {
	st, apiTok, _ := meFixture(t)
	f := newFakeTokenUM(t)
	srv, c := meServer(t, st, f)
	f.give("umd_mel", "s-mel", "mel", "editor")
	usersBefore, _ := st.CountUsers()

	// No token, an mt_ token, another audience's, an expired one: all 401
	// invalid_token, and nobody is added.
	for _, tok := range []string{"", apiTok, "umd_for_calendar", "umd_expired"} {
		if body := c.meGet("/api/v1/me/items?q=x", tok, 401); !strings.Contains(body, "invalid_token") {
			t.Errorf("token %q: %s", tok, body)
		}
	}
	if n, _ := st.CountUsers(); n != usersBefore {
		t.Errorf("refused tokens made %d users", n-usersBefore)
	}

	// A good token: the user is made like an SSO sign-in, with no session.
	f.hits.Store(0)
	now := time.Now()
	srv.meTokens.Now = func() time.Time { return now }
	c.meGet("/api/v1/me/items?q=x", "umd_mel", 200)
	c.meGet("/api/v1/me/places", "umd_mel", 200)
	if n := f.hits.Load(); n != 1 {
		t.Errorf("second call within 60 s asked User Management again: %d calls", n)
	}
	u, err := st.GetUserBySubject("s-mel")
	if err != nil || u.Username != "mel" || u.DisplayName != "Mel" || u.IsAdmin || !u.Active {
		t.Fatalf("user = %+v (%v)", u, err)
	}
	var sessions int
	if err := st.DB.QueryRow(`SELECT count(*) FROM sessions WHERE user_id = ?`, u.ID).Scan(&sessions); err != nil || sessions != 0 {
		t.Errorf("a /me call made %d sessions (%v)", sessions, err)
	}

	// After 60 s it's checked again, and a token gone bad is refused.
	now = now.Add(61 * time.Second)
	f.mu.Lock()
	delete(f.ok, "umd_mel")
	f.mu.Unlock()
	c.meGet("/api/v1/me/places", "umd_mel", 401)
	if n := f.hits.Load(); n != 2 {
		t.Errorf("calls = %d, want 2", n)
	}

	// The role follows the token, as at sign-in.
	f.give("umd_mel2", "s-mel", "mel", "admin")
	c.meGet("/api/v1/me/places", "umd_mel2", 200)
	if u, _ := st.GetUserBySubject("s-mel"); !u.IsAdmin {
		t.Error("admin role from the token wasn't applied")
	}

	// Another delegate is refused; User Management down is 503, not 401.
	f.mu.Lock()
	f.ok["umd_other"] = umtoken.Answer{User: umtoken.User{Sub: "s-x", Username: "x"}, Via: "someapp"}
	f.mu.Unlock()
	c.meGet("/api/v1/me/places", "umd_other", 403)
	f.setDown(true)
	c.meGet("/api/v1/me/places", "umd_new", 503)
	f.setDown(false)

	// Unknown /me paths are a plain 404 ("not built"), unlike /api/'s JSON one.
	if body := c.meGet("/api/v1/me/nope", "umd_mel2", 404); isJSONError(body) {
		t.Errorf("unknown /me path: %s", body)
	}
	if body := c.meGet("/api/v1/me/items/1", "umd_mel2", 404); isJSONError(body) {
		t.Errorf("/me detail paths aren't built: %s", body)
	}
	// The app-token API still works with SSO on.
	c.apiGet(apiTok, "/api/v1/items", 200, nil)
}

// The lists are what any signed-in person sees on the web pages, in the
// contract's shapes, without notes, serial numbers or other extras.
func TestMeLists(t *testing.T) {
	st, _, editorWeb := meFixture(t)
	f := newFakeTokenUM(t)
	_, c := meServer(t, st, f)
	f.give("umd_ed", "s-ed", "ed", "editor")
	f.give("umd_ad", "s-ad", "ad", "admin")

	// An editor sees these on the web.
	for _, p := range []string{"/items", "/supplies", "/places", "/products"} {
		body := editorWeb.get(p, 200)
		for _, want := range map[string][]string{"/items": {"Epson projector", "Folding chairs"}, "/supplies": {"Paper towels", "Dish soap"},
			"/places": {"Fellowship Hall", "Kitchen"}, "/products": {"Epson projector", "Folding chairs"}}[p] {
			if !strings.Contains(body, want) {
				t.Errorf("web %s for an editor lacks %q", p, want)
			}
		}
	}

	for _, tok := range []string{"umd_ed", "umd_ad"} {
		// Exact JSON field names, per the contract's examples.
		body := c.meGet("/api/v1/me/places?q=HALL", tok, 200)
		var raw map[string][]map[string]any
		json.Unmarshal([]byte(body), &raw)
		if len(raw["places"]) != 1 {
			t.Fatalf("places?q=HALL: %s", body)
		}
		checkKeys(t, "place", raw["places"][0], "id", "kind", "name", "number", "path", "bookable", "address", "tags")
		if p := raw["places"][0]; p["name"] != "Fellowship Hall" || p["path"] != "Main › Fellowship Hall" || p["kind"] != "room" {
			t.Errorf("place = %v", p)
		}
		// q matches the path too: the rooms under Main.
		if body := c.meGet("/api/v1/me/places?q="+url.QueryEscape("main ›"), tok, 200); strings.Count(body, `"id"`) != 2 {
			t.Errorf("places?q=main: %s", body)
		}
		// Room numbers, as the places page shows them.
		if body := c.meGet("/api/v1/me/places?q=104", tok, 200); !strings.Contains(body, "Kitchen") {
			t.Errorf("places?q=104: %s", body)
		}

		body = c.meGet("/api/v1/me/products?q=projector", tok, 200)
		raw = nil
		json.Unmarshal([]byte(body), &raw)
		if len(raw["products"]) != 1 {
			t.Fatalf("products: %s", body)
		}
		checkKeys(t, "product", raw["products"][0], "id", "name", "category", "total", "place_count")
		if p := raw["products"][0]; p["category"] != "AV" || p["total"] != 1.0 || p["place_count"] != 1.0 {
			t.Errorf("product = %v", p)
		}

		body = c.meGet("/api/v1/me/items?q=epson", tok, 200)
		raw = nil
		json.Unmarshal([]byte(body), &raw)
		if len(raw["items"]) != 1 {
			t.Fatalf("items: %s", body)
		}
		checkKeys(t, "item", raw["items"][0], "id", "name", "category", "quantity", "place", "manufacturer", "model", "notes", "supply", "supply_per", "units")
		it := raw["items"][0]
		place, _ := it["place"].(map[string]any)
		if it["model"] != "EB-992F" || place["path"] != "Main › Fellowship Hall" || place["id"] != 3.0 {
			t.Errorf("item = %v", it)
		}
		checkKeys(t, "item place", place, "id", "path")
		// Notes are on the web page for every signed-in person, so they're
		// here too; serial numbers stay out.
		if strings.Contains(body, "SN-SECRET") {
			t.Errorf("serial number leaked: %s", body)
		}
		if it["notes"] != "key in the office" {
			t.Errorf("item notes = %v", it["notes"])
		}

		body = c.meGet("/api/v1/me/supplies?low=1", tok, 200)
		raw = nil
		json.Unmarshal([]byte(body), &raw)
		if len(raw["supplies"]) != 1 {
			t.Fatalf("low supplies: %s", body)
		}
		checkKeys(t, "supply", raw["supplies"][0], "id", "name", "unit", "place", "on_hand", "total", "stock")
		if sp := raw["supplies"][0]; sp["name"] != "Paper towels" || sp["stock"] != "low" || sp["on_hand"] != 2.0 || sp["unit"] != "rolls" {
			t.Errorf("supply = %v", sp)
		}
		if body := c.meGet("/api/v1/me/supplies?q=soap", tok, 200); !strings.Contains(body, `"stock": "ok"`) || strings.Contains(body, "sink") {
			t.Errorf("supplies?q=soap: %s", body)
		}

		// Empty lists are [], and ?limit= is honoured.
		for _, p := range mePaths {
			if body := c.meGet(p+"?q=zzz-nothing", tok, 200); !strings.Contains(body, "[]") {
				t.Errorf("%s empty: %s", p, body)
			}
		}
		if body := c.meGet("/api/v1/me/places?limit=2", tok, 200); strings.Count(body, `"id"`) != 2 {
			t.Errorf("limit=2: %s", body)
		}
	}
}

// checkKeys fails unless obj has exactly these keys.
func checkKeys(t *testing.T, what string, obj map[string]any, keys ...string) {
	t.Helper()
	var got []string
	for k := range obj {
		got = append(got, k)
	}
	slices.Sort(got)
	slices.Sort(keys)
	if !slices.Equal(got, keys) {
		t.Errorf("%s keys = %v, want %v", what, got, keys)
	}
}

// meMaintFixture adds a furnace-filter task (recurring, using the supply
// "Furnace filters") done twice, and a one-off unscheduled task, to the
// meFixture data.
func meMaintFixture(t *testing.T, ed *client) {
	t.Helper()
	ed.post("/supplies/new", "/supplies", url.Values{"place_id": {"4"}, "name": {"Furnace filters"}, "quantity": {"5"}, "reorder_at": {"1"}}, 200) // supply 3
	ed.post("/tasks/new?item=1", "/tasks", url.Values{"item_id": {"1"}, "name": {"Change air filter"}, "uses_supply": {"1"}, "supply_id": {"3"}, "supply_amount": {"1"},
		"supply_always": {"1"}, "recurring": {"1"}, "interval_value": {"3"}, "interval_unit": {"months"}}, 200) // task 1
	ed.post("/tasks/new?item=2", "/tasks", url.Values{"item_id": {"2"}, "name": {"Wipe down"}}, 200) // task 2, unscheduled
	ed.post("/tasks/1/complete", "/tasks/1/complete", url.Values{"performed_on": {"2026-01-10"}, "took_supply": {"1"}, "supply_amount": {"1"}}, 200)
	ed.post("/tasks/1/complete", "/tasks/1/complete", url.Values{"performed_on": {"2026-04-12"}, "took_supply": {"1"}, "supply_amount": {"1"}, "cost": {"12.50"}}, 200)
}

func TestMeTasks(t *testing.T) {
	st, _, ed := meFixture(t)
	meMaintFixture(t, ed)
	f := newFakeTokenUM(t)
	_, c := meServer(t, st, f)
	f.give("umd_ed", "s-ed", "ed", "editor")
	f.give("umd_ad", "s-ad", "admin", "admin")

	get := func(tok, q string, want int) []map[string]any {
		body := c.meGet("/api/v1/me/tasks"+q, tok, want)
		var raw map[string][]map[string]any
		json.Unmarshal([]byte(body), &raw)
		return raw["tasks"]
	}
	// An editor and an admin see the same, as on the web pages.
	for _, tok := range []string{"umd_ed", "umd_ad"} {
		all := get(tok, "", 200)
		if len(all) != 2 {
			t.Fatalf("tasks = %v", all)
		}
		checkKeys(t, "task", all[0], "id", "item", "place", "name", "interval", "last_completed_on", "next_due_on", "status", "supply", "supply_amount", "supply_short", "description", "quantity", "item_notes", "units")
		if a := all[0]; a["item"] != "Epson projector" || a["name"] != "Change air filter" || a["interval"] != "Every 3 months" ||
			a["last_completed_on"] != "2026-04-12" || a["next_due_on"] != "2026-07-12" || a["status"] != "overdue" || a["supply"] != "Furnace filters" || a["supply_short"] != false {
			t.Errorf("task = %v", a)
		}
		if all[1]["status"] != "unscheduled" {
			t.Errorf("most urgent first: %v", all)
		}
		if _, leaked := all[0]["cost"]; leaked {
			t.Error("cost in tasks")
		}
	}
	// q matches a supply name, the task name, the item name; not nonsense.
	for _, q := range []string{"furnace", "AIR FILTER", "epson"} {
		if got := get("umd_ed", "?q="+url.QueryEscape(q), 200); len(got) != 1 || got[0]["name"] != "Change air filter" {
			t.Errorf("q=%s: %v", q, got)
		}
	}
	if got := get("umd_ed", "?q=zzz", 200); len(got) != 0 {
		t.Errorf("q=zzz: %v", got)
	}
	// status filter, and place text.
	if got := get("umd_ed", "?status=overdue", 200); len(got) != 1 || got[0]["status"] != "overdue" {
		t.Errorf("status=overdue: %v", got)
	}
	if got := get("umd_ed", "?status=unscheduled", 200); len(got) != 1 || got[0]["name"] != "Wipe down" {
		t.Errorf("status=unscheduled: %v", got)
	}
	if got := get("umd_ed", "?status=upcoming", 200); len(got) != 0 {
		t.Errorf("status=upcoming: %v", got)
	}
	if got := get("umd_ed", "?place=kitchen", 200); len(got) != 1 || got[0]["item"] != "Folding chairs" {
		t.Errorf("place=kitchen: %v", got)
	}
	get("umd_ed", "?status=bogus", 400)
	// Gate: no token and another audience.
	for _, tok := range []string{"", "umd_for_calendar"} {
		c.meGet("/api/v1/me/tasks", tok, 401)
		c.meGet("/api/v1/me/history", tok, 401)
	}
}

func TestMeHistory(t *testing.T) {
	st, _, ed := meFixture(t)
	meMaintFixture(t, ed)
	f := newFakeTokenUM(t)
	_, c := meServer(t, st, f)
	f.give("umd_ed", "s-ed", "ed", "editor")
	f.give("umd_ad", "s-ad", "admin", "admin")

	get := func(tok, q string, want int) []map[string]any {
		body := c.meGet("/api/v1/me/history"+q, tok, want)
		if strings.Contains(body, "12.50") || strings.Contains(body, "cost") {
			t.Errorf("cost leaked: %s", body)
		}
		var raw map[string][]map[string]any
		json.Unmarshal([]byte(body), &raw)
		return raw["history"]
	}
	for _, tok := range []string{"umd_ed", "umd_ad"} {
		all := get(tok, "", 200)
		if len(all) != 2 {
			t.Fatalf("history = %v", all)
		}
		checkKeys(t, "log", all[0], "id", "performed_on", "item", "place", "task", "kind", "replaced", "units", "supply_used", "performed_by", "notes")
		if all[0]["performed_on"] != "2026-04-12" || all[1]["performed_on"] != "2026-01-10" {
			t.Errorf("newest first: %v", all)
		}
		if all[0]["task"] != "Change air filter" || all[0]["item"] != "Epson projector" {
			t.Errorf("entry = %v", all[0])
		}
	}
	// q finds supply-name matches ("furnace"), task names, and not others.
	if got := get("umd_ed", "?q=furnace", 200); len(got) != 2 {
		t.Errorf("q=furnace: %v", got)
	}
	if got := get("umd_ed", "?q=filter", 200); len(got) != 2 {
		t.Errorf("q=filter: %v", got)
	}
	if got := get("umd_ed", "?q=chairs", 200); len(got) != 0 {
		t.Errorf("q=chairs: %v", got)
	}
	// since, item, limit.
	if got := get("umd_ed", "?since=2026-03-01", 200); len(got) != 1 || got[0]["performed_on"] != "2026-04-12" {
		t.Errorf("since: %v", got)
	}
	if got := get("umd_ed", "?since=2026-04-12", 200); len(got) != 1 {
		t.Errorf("since is inclusive: %v", got)
	}
	if got := get("umd_ed", "?item=2", 200); len(got) != 0 {
		t.Errorf("item=2: %v", got)
	}
	if got := get("umd_ed", "?limit=1", 200); len(got) != 1 {
		t.Errorf("limit: %v", got)
	}
	get("umd_ed", "?since=yesterday", 400)
	get("umd_ed", "?item=x", 400)
}

// With no q and no low, supplies lists them all (the stock overview).
func TestMeSuppliesAll(t *testing.T) {
	st, _, _ := meFixture(t)
	f := newFakeTokenUM(t)
	_, c := meServer(t, st, f)
	f.give("umd_ed", "s-ed", "ed", "editor")
	if body := c.meGet("/api/v1/me/supplies", "umd_ed", 200); strings.Count(body, `"unit"`) != 2 || !strings.Contains(body, "Dish soap") || !strings.Contains(body, "Paper towels") {
		t.Errorf("supplies: %s", body)
	}
}

// A group item (four air returns in one room) shows its count, each unit's
// label and when its filter was last replaced, how many filters a check
// uses, and which units a history entry was about.
func TestMeUnits(t *testing.T) {
	st, _, ed := meFixture(t)
	meMaintFixture(t, ed) // supply 3: Furnace filters
	it := &store.Item{PlaceID: 3, Name: "Air Return", Category: "HVAC", Quantity: 4, SupplyID: 3, SupplyPer: 1, Notes: "covers lift off"}
	if err := st.SaveItem(it, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveUnits(it.ID, []store.Unit{{Number: 1, Label: "Northwest"}, {Number: 2, Label: "Southwest"},
		{Number: 3, Label: "Northeast"}, {Number: 4, Label: "Southeast"}}); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(it.ID, 10)
	ed.post("/tasks/new?item="+id, "/tasks", url.Values{"item_id": {id}, "name": {"Check Filters"}, "description": {"Look at all four"},
		"uses_supply": {"1"}, "supply_id": {"3"}, "supply_amount": {"4"}, "recurring": {"1"}, "interval_value": {"1"}, "interval_unit": {"months"}}, 200)
	if err := st.RecordMaintenance(store.Completion{ItemID: it.ID, Kind: "replaced", Replaced: 2, Units: []int{1, 3}, PerformedOn: "2026-11-07"}); err != nil {
		t.Fatal(err)
	}
	f := newFakeTokenUM(t)
	_, c := meServer(t, st, f)
	f.give("umd_ed", "s-ed", "ed", "editor")

	var tasks map[string][]map[string]any
	json.Unmarshal([]byte(c.meGet("/api/v1/me/tasks?q=check+filters", "umd_ed", 200)), &tasks)
	if len(tasks["tasks"]) != 1 {
		t.Fatalf("tasks = %v", tasks)
	}
	task := tasks["tasks"][0]
	if task["quantity"] != 4.0 || task["supply_amount"] != 4.0 || task["description"] != "Look at all four" || task["item_notes"] != "covers lift off" {
		t.Errorf("task = %v", task)
	}
	units, _ := task["units"].([]any)
	if len(units) != 4 {
		t.Fatalf("units = %v", task["units"])
	}
	u1, u2 := units[0].(map[string]any), units[1].(map[string]any)
	if u1["label"] != "Northwest" || u1["last_replaced"] != "2026-11-07" || u2["label"] != "Southwest" || u2["last_replaced"] != "" {
		t.Errorf("units = %v", units)
	}
	checkKeys(t, "unit", u1, "number", "id", "label", "last_replaced")

	var hist map[string][]map[string]any
	json.Unmarshal([]byte(c.meGet("/api/v1/me/history?item="+id, "umd_ed", 200)), &hist)
	if len(hist["history"]) != 1 {
		t.Fatalf("history = %v", hist)
	}
	if h := hist["history"][0]; h["units"] != "#1 – Northwest; #3 – Northeast" || h["replaced"] != "2 × Furnace filters" {
		t.Errorf("history = %v", h)
	}

	var items map[string][]map[string]any
	json.Unmarshal([]byte(c.meGet("/api/v1/me/items?q=air+return", "umd_ed", 200)), &items)
	if len(items["items"]) != 1 || items["items"][0]["supply"] != "Furnace filters" || len(items["items"][0]["units"].([]any)) != 4 {
		t.Errorf("items = %v", items)
	}
}
