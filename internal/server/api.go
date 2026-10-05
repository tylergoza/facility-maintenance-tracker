package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// The API lets other apps (an event or production planner) find what
// there is and where: read-only JSON under /api/v1/, authorised by a
// token an admin makes on the API tokens page, sent as
// "Authorization: Bearer mt_…". Session cookies aren't accepted, so the
// API needs no CSRF protection.

func (s *Server) apiRoutes(mux *http.ServeMux) {
	api := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireToken(h)) }
	api("GET /api/v1/products", s.apiProducts)
	api("GET /api/v1/products/{id}", s.apiProduct)
	api("GET /api/v1/items", s.apiItems)
	api("GET /api/v1/items/{id}", s.apiItem)
	api("GET /api/v1/supplies", s.apiSupplies)
	api("GET /api/v1/supplies/{id}", s.apiSupply)
	api("GET /api/v1/places", s.apiPlaces)
	api("GET /api/v1/places/{id}", s.apiPlace)
	api("/api/", func(w http.ResponseWriter, r *http.Request) { apiError(w, http.StatusNotFound, "no such endpoint") })
}

func (s *Server) requireToken(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
			apiError(w, http.StatusUnauthorized, "send an API token as: Authorization: Bearer <token>")
			return
		}
		if _, err := s.store.UseAPIToken(strings.TrimSpace(token)); errors.Is(err, store.ErrNotFound) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="api", error="invalid_token"`)
			apiError(w, http.StatusUnauthorized, "that API token isn't valid; it may have been revoked")
			return
		} else if err != nil {
			s.apiServerError(w, r, err)
			return
		}
		next(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) apiServerError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	s.log.Error("api", "path", r.URL.Path, "err", err)
	apiError(w, http.StatusInternalServerError, "something went wrong")
}

// apiFilters reads the query parameters several lists share. A place
// that doesn't exist is an error rather than matching nothing.
type apiFilters struct {
	Query, Tag string
	PlaceID    int64
	Direct     bool
}

func (s *Server) apiFilters(w http.ResponseWriter, r *http.Request) (apiFilters, bool) {
	q := r.URL.Query()
	f := apiFilters{Query: q.Get("q"), Tag: q.Get("tag"), Direct: apiBool(q.Get("direct"))}
	if v := q.Get("place"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			_, err = s.store.GetPlace(id)
		}
		if err != nil {
			apiError(w, http.StatusBadRequest, "place "+v+" doesn't exist")
			return f, false
		}
		f.PlaceID = id
	}
	return f, true
}

func apiBool(v string) bool { return v == "1" || v == "true" }

// JSON shapes ----------------------------------------------------------------

type apiPlaceRef struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
}

type apiProductJSON struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	// Counted products' items have no numbered units, only a quantity.
	Counted    bool   `json:"counted"`
	Notes      string `json:"notes"`
	Total      int    `json:"total"`
	ItemCount  int    `json:"item_count"`
	PlaceCount int    `json:"place_count"`
}

type apiUnitJSON struct {
	Number int    `json:"number"`
	ID     string `json:"id"`
	Note   string `json:"note,omitempty"`
}

type apiItemJSON struct {
	ID           int64         `json:"id"`
	ProductID    int64         `json:"product_id"`
	Name         string        `json:"name"`
	Category     string        `json:"category"`
	Counted      bool          `json:"counted"`
	Portable     bool          `json:"portable"`
	Quantity     int           `json:"quantity"`
	Place        apiPlaceRef   `json:"place"`
	Manufacturer string        `json:"manufacturer"`
	Model        string        `json:"model"`
	SerialNumber string        `json:"serial_number"`
	InstallDate  string        `json:"install_date"`
	Notes        string        `json:"notes"`
	Units        []apiUnitJSON `json:"units,omitempty"`
}

type apiSupplyJSON struct {
	ID        int64       `json:"id"`
	Name      string      `json:"name"`
	Unit      string      `json:"unit"`
	Place     apiPlaceRef `json:"place"`
	Reusable  bool        `json:"reusable"`
	OnHand    int         `json:"on_hand"`
	InUse     int         `json:"in_use"`
	Cleaning  int         `json:"cleaning"`
	Total     int         `json:"total"`
	ReorderAt int         `json:"reorder_at"`
	Stock     string      `json:"stock"` // "ok", "low" or "out"
	Notes     string      `json:"notes"`
	// Someone asked for more; null when nobody has.
	Requested *apiRequestJSON `json:"requested"`
}

type apiRequestJSON struct {
	At   string `json:"at"` // UTC, "YYYY-MM-DD HH:MM:SS"
	Note string `json:"note"`
}

type apiPlaceJSON struct {
	ID       int64    `json:"id"`
	ParentID *int64   `json:"parent_id"`
	Kind     string   `json:"kind"`
	Number   string   `json:"number"`
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Address  string   `json:"address"`
	Notes    string   `json:"notes"`
	Tags     []string `json:"tags"`
	// Shared areas: the other places they're also in.
	AlsoIn []int64 `json:"also_in"`
}

func productJSON(p store.Product) apiProductJSON {
	return apiProductJSON{ID: p.ID, Name: p.Name, Category: p.Category, Counted: p.Counted, Notes: p.Notes,
		Total: p.Total, ItemCount: p.ItemCount, PlaceCount: p.PlaceCount}
}

func itemJSON(i store.Item) apiItemJSON {
	return apiItemJSON{ID: i.ID, ProductID: i.ProductID, Name: i.Name, Category: i.Category, Counted: i.Counted, Portable: i.Portable,
		Quantity: i.Quantity, Place: apiPlaceRef{i.PlaceID, i.Place.Path}, Manufacturer: i.Manufacturer, Model: i.Model,
		SerialNumber: i.SerialNumber, InstallDate: i.InstallDate, Notes: i.Notes}
}

func itemsJSON(items []store.Item) []apiItemJSON {
	out := make([]apiItemJSON, len(items))
	for k, i := range items {
		out[k] = itemJSON(i)
	}
	return out
}

func supplyJSON(sp store.Supply) apiSupplyJSON {
	out := apiSupplyJSON{ID: sp.ID, Name: sp.Name, Unit: sp.Unit, Place: apiPlaceRef{sp.PlaceID, sp.Place.Path}, Reusable: sp.Reusable,
		OnHand: sp.Quantity, InUse: sp.InUse, Cleaning: sp.Cleaning, Total: sp.Total(), ReorderAt: sp.ReorderAt, Stock: sp.Stock(), Notes: sp.Notes}
	if sp.Requested() {
		out.Requested = &apiRequestJSON{At: sp.RequestedAt, Note: sp.RequestNote}
	}
	return out
}

func suppliesJSON(supplies []store.Supply) []apiSupplyJSON {
	out := make([]apiSupplyJSON, len(supplies))
	for k, sp := range supplies {
		out[k] = supplyJSON(sp)
	}
	return out
}

func placeJSON(p store.Place) apiPlaceJSON {
	out := apiPlaceJSON{ID: p.ID, Kind: p.Kind, Number: p.Number, Name: p.Name, Path: p.Path, Address: p.Address, Notes: p.Notes,
		Tags: p.Tags, AlsoIn: p.AlsoIn}
	if p.ParentID != 0 {
		out.ParentID = &p.ParentID
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if out.AlsoIn == nil {
		out.AlsoIn = []int64{}
	}
	return out
}

// Endpoints ------------------------------------------------------------------

// apiProducts: GET /api/v1/products?q=&category=&place=&tag=&portable=1&counted=1
// Totals count only items in the place or tag asked about.
func (s *Server) apiProducts(w http.ResponseWriter, r *http.Request) {
	f, ok := s.apiFilters(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	products, err := s.store.ListProducts(store.ProductFilter{Query: f.Query, Category: q.Get("category"), PlaceID: f.PlaceID, Tag: f.Tag,
		PortableOnly: apiBool(q.Get("portable")), CountedOnly: apiBool(q.Get("counted"))})
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := make([]apiProductJSON, len(products))
	for k, p := range products {
		out[k] = productJSON(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": out})
}

// apiProduct: GET /api/v1/products/{id}, with where every one of it is.
func (s *Server) apiProduct(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProduct(pathID(r))
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	items, err := s.store.ListItems(store.ItemFilter{ProductID: p.ID})
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		apiProductJSON
		Items []apiItemJSON `json:"items"`
	}{productJSON(*p), itemsJSON(items)})
}

// apiItems: GET /api/v1/items?q=&place=&direct=1&tag=&product=&portable=1
func (s *Server) apiItems(w http.ResponseWriter, r *http.Request) {
	f, ok := s.apiFilters(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	product, _ := strconv.ParseInt(q.Get("product"), 10, 64)
	items, err := s.store.ListItems(store.ItemFilter{Query: f.Query, PlaceID: f.PlaceID, Direct: f.Direct, Tag: f.Tag,
		ProductID: product, PortableOnly: apiBool(q.Get("portable"))})
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": itemsJSON(items)})
}

// apiItem: GET /api/v1/items/{id}, with its units unless it's counted.
func (s *Server) apiItem(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := itemJSON(*item)
	if !item.Counted {
		units, err := s.store.ItemUnits(item.ID, item.Quantity, s.todayTime())
		if err != nil {
			s.apiServerError(w, r, err)
			return
		}
		for _, u := range units {
			out.Units = append(out.Units, apiUnitJSON{Number: u.Number, ID: u.ID(), Note: u.Label})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// apiSupplies: GET /api/v1/supplies?q=&place=&direct=1&tag=&low=1
func (s *Server) apiSupplies(w http.ResponseWriter, r *http.Request) {
	f, ok := s.apiFilters(w, r)
	if !ok {
		return
	}
	supplies, err := s.store.ListSupplies(store.SupplyFilter{Query: f.Query, PlaceID: f.PlaceID, Direct: f.Direct, Tag: f.Tag,
		LowOnly: apiBool(r.URL.Query().Get("low"))})
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"supplies": suppliesJSON(supplies)})
}

func (s *Server) apiSupply(w http.ResponseWriter, r *http.Request) {
	sp, err := s.store.GetSupply(pathID(r))
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, supplyJSON(*sp))
}

// apiPlaces: GET /api/v1/places?tag=, every place in tree order.
func (s *Server) apiPlaces(w http.ResponseWriter, r *http.Request) {
	tree, err := s.store.Places()
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	places := tree.All()
	if tag := r.URL.Query().Get("tag"); tag != "" {
		places = tree.Tagged(tag)
	}
	out := make([]apiPlaceJSON, len(places))
	for k, p := range places {
		out[k] = placeJSON(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"places": out})
}

// apiPlace: GET /api/v1/places/{id}?direct=1, with the items and supplies
// in it and, unless direct, anywhere inside it.
func (s *Server) apiPlace(w http.ResponseWriter, r *http.Request) {
	tree, err := s.store.Places()
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	p, ok := tree.Get(pathID(r))
	if !ok {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	direct := apiBool(r.URL.Query().Get("direct"))
	items, err := s.store.ListItems(store.ItemFilter{PlaceID: p.ID, Direct: direct})
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	supplies, err := s.store.ListSupplies(store.SupplyFilter{PlaceID: p.ID, Direct: direct})
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	children := []int64{}
	for _, c := range tree.Children(p.ID) {
		children = append(children, c.ID)
	}
	writeJSON(w, http.StatusOK, struct {
		apiPlaceJSON
		Children []int64         `json:"children"`
		Items    []apiItemJSON   `json:"items"`
		Supplies []apiSupplyJSON `json:"supplies"`
	}{placeJSON(p), children, itemsJSON(items), suppliesJSON(supplies)})
}
