package server

import (
	"cmp"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
	"github.com/tylergoza/facility-maintenance-tracker/internal/umtoken"
)

// The per-person read API, /api/v1/me/…, for the church MCP server. The
// gate (token check, local user) is me_auth.go; the shapes are
// APP_CONTRACTS.md in ../mcp_server (Maintenance tracker).
//
// Every signed-in person, editor or admin, sees all places, products,
// items and supplies on the web pages, so everyone the gate lets in gets
// the same lists here. They are the app-token API's lists (api.go, same
// queries) cut down to the fields the contract names: no notes, serial
// numbers, units or supply requests, which the AI tools don't use.

// meMaxItems bounds every list (the contract's cap).
const meMaxItems = 100

func (s *Server) meRoutes(mux *http.ServeMux) {
	s.meRoute(mux, "GET /api/v1/me/places", s.mePlaces)
	s.meRoute(mux, "GET /api/v1/me/products", s.meProducts)
	s.meRoute(mux, "GET /api/v1/me/items", s.meItems)
	s.meRoute(mux, "GET /api/v1/me/supplies", s.meSupplies)
	s.meRoute(mux, "GET /api/v1/me/tasks", s.meTasks)
	s.meRoute(mux, "GET /api/v1/me/history", s.meHistory)
	s.meRoute(mux, "GET /api/v1/me/problems", s.meProblems)
	s.meCatchAll(mux)
}

// meUMConfig is the live single sign-on connection; the /me API is off
// without it.
func (s *Server) meUMConfig() (umtoken.Config, bool) {
	c := s.SSO()
	if !c.Enabled() || c.ClientID == "" {
		return umtoken.Config{}, false
	}
	return umtoken.Config{URL: c.InternalURL, ClientID: c.ClientID, ClientSecret: c.ClientSecret}, true
}

// meLocalUser is what the SSO callback and grant checks do (sso.go).
func (s *Server) meLocalUser(u umtoken.User) (*store.User, error) {
	return s.store.UpsertSSOUser(u.Sub, u.Username, u.Name, u.Role == "admin")
}

// meLimit is ?limit= within 1..meMaxItems, meMaxItems when not given.
func meLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 || n > meMaxItems {
		return meMaxItems
	}
	return n
}

func meQuery(r *http.Request) string { return strings.TrimSpace(r.URL.Query().Get("q")) }

type mePlaceJSON struct {
	ID       int64    `json:"id"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Number   string   `json:"number"`
	Path     string   `json:"path"`
	Bookable bool     `json:"bookable"`
	Address  string   `json:"address"`
	Tags     []string `json:"tags"`
}

type meProductJSON struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	Total      int    `json:"total"`
	PlaceCount int    `json:"place_count"`
}

type meItemJSON struct {
	ID           int64        `json:"id"`
	Name         string       `json:"name"`
	Category     string       `json:"category"`
	Quantity     int          `json:"quantity"`
	Place        apiPlaceRef  `json:"place"`
	Manufacturer string       `json:"manufacturer"`
	Model        string       `json:"model"`
	Notes        string       `json:"notes"`
	Supply       string       `json:"supply"`     // what each one uses, e.g. "14x30 Filter"; "" if none
	SupplyPer    int          `json:"supply_per"` // how many of it each one takes
	Units        []meUnitJSON `json:"units"`      // numbered units of a group item (Quantity > 1), else []
}

// meUnitJSON is one numbered unit of a group item, e.g. one of the four
// air returns in the fellowship hall, with when its supply was last
// replaced (from history entries that name the unit).
type meUnitJSON struct {
	Number       int    `json:"number"`
	ID           string `json:"id"`    // its tag, or its number
	Label        string `json:"label"` // where it is, e.g. "Northwest (away from sanctuary)"
	LastReplaced string `json:"last_replaced"`
}

// meUnits loads items' units once per item for one response.
type meUnits struct {
	s     *Server
	items map[int64]*store.Item
	units map[int64][]meUnitJSON
}

func (s *Server) newMeUnits() *meUnits {
	return &meUnits{s: s, items: map[int64]*store.Item{}, units: map[int64][]meUnitJSON{}}
}

// item returns the item (nil if it can't be read) and its units.
func (m *meUnits) item(id int64) (*store.Item, []meUnitJSON) {
	if it, ok := m.items[id]; ok {
		return it, m.units[id]
	}
	it, err := m.s.store.GetItem(id)
	if err != nil {
		m.items[id] = nil
		return nil, []meUnitJSON{}
	}
	out := []meUnitJSON{}
	if it.HasUnits() {
		if units, err := m.s.store.ItemUnits(it.ID, it.Quantity, m.s.todayTime()); err == nil {
			for _, u := range units {
				out = append(out, meUnitJSON{Number: u.Number, ID: u.ID(), Label: u.Label, LastReplaced: u.LastReplaced})
			}
		}
	}
	m.items[id], m.units[id] = it, out
	return it, out
}

// names renders units by number with their labels, e.g.
// "#1 – Northwest (away from sanctuary)"; "" for none.
func (m *meUnits) names(itemID int64, numbers []int) string {
	if len(numbers) == 0 {
		return ""
	}
	_, units := m.item(itemID)
	parts := make([]string, 0, len(numbers))
	for _, n := range numbers {
		name := "#" + strconv.Itoa(n)
		if n >= 1 && n <= len(units) && units[n-1].Label != "" {
			name += " – " + units[n-1].Label
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, "; ")
}

type meSupplyJSON struct {
	ID     int64       `json:"id"`
	Name   string      `json:"name"`
	Unit   string      `json:"unit"`
	Place  apiPlaceRef `json:"place"`
	OnHand int         `json:"on_hand"`
	Total  int         `json:"total"`
	Stock  string      `json:"stock"` // "ok", "low" or "out"
}

// mePlaces: GET /api/v1/me/places?q=, in tree order, q matching the
// name, room number or path (any case).
func (s *Server) mePlaces(w http.ResponseWriter, r *http.Request) {
	tree, err := s.store.Places()
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	q, limit := strings.ToLower(meQuery(r)), meLimit(r)
	out := []mePlaceJSON{}
	for _, p := range tree.All() {
		if len(out) == limit {
			break
		}
		if q != "" && !strings.Contains(strings.ToLower(p.Name+"\x00"+p.Number+"\x00"+p.Path), q) {
			continue
		}
		j := placeJSON(p)
		out = append(out, mePlaceJSON{ID: j.ID, Kind: j.Kind, Name: j.Name, Number: j.Number, Path: j.Path,
			Bookable: j.Bookable, Address: j.Address, Tags: j.Tags})
	}
	writeJSON(w, http.StatusOK, map[string]any{"places": out})
}

// meProducts: GET /api/v1/me/products?q=, as the Products page searches.
func (s *Server) meProducts(w http.ResponseWriter, r *http.Request) {
	products, err := s.store.ListProducts(store.ProductFilter{Query: meQuery(r)})
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := []meProductJSON{}
	for _, p := range products[:min(len(products), meLimit(r))] {
		j := productJSON(p)
		out = append(out, meProductJSON{ID: j.ID, Name: j.Name, Category: j.Category, Total: j.Total, PlaceCount: j.PlaceCount})
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": out})
}

// meItems: GET /api/v1/me/items?q=, as the Items page searches.
func (s *Server) meItems(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListItems(store.ItemFilter{Query: meQuery(r)})
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := []meItemJSON{}
	mu := s.newMeUnits()
	for _, i := range items[:min(len(items), meLimit(r))] {
		j := itemJSON(i)
		_, units := mu.item(i.ID)
		supply := ""
		if i.SupplyID != 0 {
			supply = i.Supply.Name
		}
		out = append(out, meItemJSON{ID: j.ID, Name: j.Name, Category: j.Category, Quantity: j.Quantity, Place: j.Place,
			Manufacturer: j.Manufacturer, Model: j.Model, Notes: i.Notes, Supply: supply, SupplyPer: i.SupplyPer, Units: units})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// meSupplies: GET /api/v1/me/supplies?q=&low=1, low=1 keeping only those
// low or out.
func (s *Server) meSupplies(w http.ResponseWriter, r *http.Request) {
	supplies, err := s.store.ListSupplies(store.SupplyFilter{Query: meQuery(r), LowOnly: apiBool(r.URL.Query().Get("low"))})
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := []meSupplyJSON{}
	for _, sp := range supplies[:min(len(supplies), meLimit(r))] {
		j := supplyJSON(sp)
		out = append(out, meSupplyJSON{ID: j.ID, Name: j.Name, Unit: j.Unit, Place: j.Place, OnHand: j.OnHand, Total: j.Total, Stock: j.Stock})
	}
	writeJSON(w, http.StatusOK, map[string]any{"supplies": out})
}

type meTaskJSON struct {
	ID              int64        `json:"id"`
	Item            string       `json:"item"`
	Place           apiPlaceRef  `json:"place"`
	Name            string       `json:"name"`
	Interval        string       `json:"interval"`
	LastCompletedOn string       `json:"last_completed_on"`
	NextDueOn       string       `json:"next_due_on"`
	Status          string       `json:"status"` // overdue, due-soon, upcoming or unscheduled
	Supply          string       `json:"supply"`
	SupplyAmount    int          `json:"supply_amount"` // how many of the supply one completion uses; 0 if none
	SupplyShort     bool         `json:"supply_short"`
	Description     string       `json:"description"` // the task's own instructions
	Quantity        int          `json:"quantity"`    // how many the item counts, e.g. 4 air returns
	ItemNotes       string       `json:"item_notes"`
	Units           []meUnitJSON `json:"units"` // the item's numbered units, each checked by this task; [] if one
}

type meHistoryJSON struct {
	ID          int64       `json:"id"`
	PerformedOn string      `json:"performed_on"`
	Item        string      `json:"item"`
	Place       apiPlaceRef `json:"place"`
	Task        string      `json:"task"`
	Kind        string      `json:"kind"` // work, replaced or moved
	Replaced    string      `json:"replaced"`
	Units       string      `json:"units"`       // which of a group item's units, e.g. "#1 – Northwest; #3 – Northeast"; "" for all/one
	SupplyUsed  string      `json:"supply_used"` // stock taken, e.g. "4 × 14x30 Filter"; "" if none
	PerformedBy string      `json:"performed_by"`
	Notes       string      `json:"notes"`
}

// meTasks: GET /api/v1/me/tasks?q=&status=&place=&limit=. Active tasks,
// most urgent first (the Maintenance page's order: overdue, due soon,
// upcoming, unscheduled; soonest due first within each). q matches the
// item (product) name, task name or supply name; place is text matched
// against the item's place path. Every signed-in person sees maintenance
// on the web (the pages are behind login only, not admin-only).
func (s *Server) meTasks(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	switch store.Status(status) {
	case "", store.StatusOverdue, store.StatusDueSoon, store.StatusUpcoming, store.StatusUnscheduled:
	default:
		apiError(w, http.StatusBadRequest, "status must be overdue, due-soon, upcoming or unscheduled")
		return
	}
	tasks, err := s.store.ListTasks(store.TaskFilter{ActiveOnly: true, Query: meQuery(r)})
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	place := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("place")))
	type ranked struct {
		t  store.Task
		st store.Status
	}
	var keep []ranked
	for _, t := range tasks {
		st := s.status(t.NextDueOn)
		if status != "" && string(st) != status {
			continue
		}
		if place != "" && !strings.Contains(strings.ToLower(t.Place.Path), place) {
			continue
		}
		keep = append(keep, ranked{t, st})
	}
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].st.Rank() < keep[j].st.Rank() })
	out := []meTaskJSON{}
	mu := s.newMeUnits()
	for _, k := range keep[:min(len(keep), meLimit(r))] {
		t := k.t
		qty, notes := 1, ""
		it, units := mu.item(t.ItemID)
		if it != nil {
			qty, notes = it.Quantity, it.Notes
		}
		amount := 0
		if t.SupplyID != 0 {
			amount = t.SupplyAmount
		}
		out = append(out, meTaskJSON{ID: t.ID, Item: t.ItemName, Place: apiPlaceRef{t.PlaceID, t.Place.Path}, Name: t.Name,
			Interval: t.IntervalLabel(), LastCompletedOn: t.LastCompletedOn, NextDueOn: t.NextDueOn, Status: string(k.st),
			Supply: t.Supply.Name, SupplyAmount: amount, SupplyShort: t.SupplyShort(), Description: t.Description,
			Quantity: qty, ItemNotes: notes, Units: units})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

// meHistory: GET /api/v1/me/history?q=&item=&since=YYYY-MM-DD&limit=, the
// History page's entries (newest first). q matches item (product), task or
// supply name; item is an item id. Cost is left out: the AI tools don't
// use it.
func (s *Server) meHistory(w http.ResponseWriter, r *http.Request) {
	since := strings.TrimSpace(r.URL.Query().Get("since"))
	if since != "" {
		if _, err := time.Parse(store.DateLayout, since); err != nil {
			apiError(w, http.StatusBadRequest, "since must be a date like 2026-01-31")
			return
		}
	}
	f := store.LogFilter{Query: meQuery(r), Since: since, Limit: meLimit(r)}
	if v := strings.TrimSpace(r.URL.Query().Get("item")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			apiError(w, http.StatusBadRequest, "item must be an item id")
			return
		}
		f.ItemID = id
	}
	logs, err := s.store.ListLogs(f)
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := []meHistoryJSON{}
	mu := s.newMeUnits()
	for _, l := range logs {
		rep := ""
		// Replacements also happen during task work (kind "work"), e.g.
		// two filters swapped while doing "Check Filters".
		if l.Replaced > 0 {
			rep = strconv.Itoa(l.Replaced)
			if l.SupplyName != "" {
				rep += " × " + l.SupplyName
			}
		}
		by := l.PerformedBy
		if by == "" {
			by = l.CreatedBy
		}
		out = append(out, meHistoryJSON{ID: l.ID, PerformedOn: l.PerformedOn, Item: l.ItemName, Place: apiPlaceRef{l.Place.ID, l.Place.Path},
			Task: l.TaskName, Kind: l.Kind, Replaced: rep, Units: mu.names(l.ItemID, l.Units), SupplyUsed: l.SupplyUsed,
			PerformedBy: by, Notes: l.Notes})
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": out})
}

type meProblemJSON struct {
	ID         int64                 `json:"id"`
	Title      string                `json:"title"`
	Details    string                `json:"details"`
	Status     string                `json:"status"` // open, in_progress or resolved
	Item       string                `json:"item"`   // "" when not tied to an item
	Unit       string                `json:"unit"`   // which of a group item's units, e.g. "#2 – left of the stage"; "" if not set
	Place      apiPlaceRef           `json:"place"`
	AssignedTo string                `json:"assigned_to"`
	ReportedBy string                `json:"reported_by"`
	CreatedAt  string                `json:"created_at"` // RFC 3339, UTC
	UpdatedAt  string                `json:"updated_at"`
	ResolvedAt string                `json:"resolved_at"` // "" until resolved
	Updates    []meProblemUpdateJSON `json:"updates"`     // oldest first, at most the last 10
}

type meProblemUpdateJSON struct {
	At         string `json:"at"` // RFC 3339, UTC
	By         string `json:"by"`
	Status     string `json:"status"`      // new status; "" if unchanged
	AssignedTo string `json:"assigned_to"` // new assignee when it changed ("nobody" when unassigned); "" otherwise
	Note       string `json:"note"`
}

// meUTC turns the store's UTC "YYYY-MM-DD HH:MM:SS" into RFC 3339.
func meUTC(s string) string {
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return s
}

// meProblems: GET /api/v1/me/problems?q=&status=active|open|in_progress|resolved|all&place=&limit=.
// Problem reports as on the Problems page (every signed-in person sees
// them): active ones first (open, then in progress), newest first. status
// defaults to active. q matches the title, details, item name or place.
// The reporter's phone or email is never sent: visitors' contact details
// stay in the app.
func (s *Server) meProblems(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	switch status {
	case "":
		status = "active"
	case "all":
		status = ""
	case "active", store.ProblemOpen, store.ProblemInProgress, store.ProblemResolved:
	default:
		apiError(w, http.StatusBadRequest, "status must be active, open, in_progress, resolved or all")
		return
	}
	problems, err := s.store.ListProblems(store.ProblemFilter{Status: status})
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	q := strings.ToLower(meQuery(r))
	place := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("place")))
	out := []meProblemJSON{}
	for _, p := range problems {
		if len(out) >= meLimit(r) {
			break
		}
		if q != "" && !strings.Contains(strings.ToLower(p.Title+"\n"+p.Details+"\n"+p.ItemName+"\n"+p.Place.Path), q) {
			continue
		}
		if place != "" && !strings.Contains(strings.ToLower(p.Place.Path), place) {
			continue
		}
		ups, err := s.store.ListProblemUpdates(p.ID)
		if err != nil {
			s.apiServerError(w, r, err)
			return
		}
		updates := []meProblemUpdateJSON{}
		for _, u := range ups[max(0, len(ups)-10):] {
			assigned := ""
			if u.Assigned {
				assigned = cmp.Or(u.AssignedName, "nobody")
			}
			updates = append(updates, meProblemUpdateJSON{At: meUTC(u.CreatedAt), By: u.CreatedBy, Status: u.Status, AssignedTo: assigned, Note: u.Note})
		}
		out = append(out, meProblemJSON{ID: p.ID, Title: p.Title, Details: p.Details, Status: p.Status, Item: p.ItemName,
			Unit: p.UnitName(), Place: apiPlaceRef{p.PlaceID, p.Place.Path}, AssignedTo: p.AssignedName, ReportedBy: p.ReporterName,
			CreatedAt: meUTC(p.CreatedAt), UpdatedAt: meUTC(p.UpdatedAt), ResolvedAt: meUTC(p.ResolvedAt), Updates: updates})
	}
	writeJSON(w, http.StatusOK, map[string]any{"problems": out})
}
