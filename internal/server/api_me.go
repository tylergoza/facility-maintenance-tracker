package server

import (
	"net/http"
	"strconv"
	"strings"

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
	ID           int64       `json:"id"`
	Name         string      `json:"name"`
	Category     string      `json:"category"`
	Quantity     int         `json:"quantity"`
	Place        apiPlaceRef `json:"place"`
	Manufacturer string      `json:"manufacturer"`
	Model        string      `json:"model"`
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
	for _, i := range items[:min(len(items), meLimit(r))] {
		j := itemJSON(i)
		out = append(out, meItemJSON{ID: j.ID, Name: j.Name, Category: j.Category, Quantity: j.Quantity, Place: j.Place,
			Manufacturer: j.Manufacturer, Model: j.Model})
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
