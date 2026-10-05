package server

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"rsc.io/qr"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// qrSVG renders text as a QR code in SVG, so it prints sharply at any
// size. Medium error correction survives a scuffed or partly covered sticker.
func qrSVG(text string) (template.HTML, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4 // blank border scanners need, in modules
	var path strings.Builder
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&path, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	n := code.Size + 2*quiet
	// The path holds only numbers and fixed letters, and the label is
	// escaped, so the markup is safe to emit as-is.
	return template.HTML(fmt.Sprintf(
		`<svg class="qr" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="QR code for %s">`+
			`<rect width="%d" height="%d" fill="#fff"/><path d="%s" fill="#000"/></svg>`,
		n, n, template.HTMLEscapeString(text), n, n, path.String())), nil
}

// qrCard is one printable card: a QR code with what it's for.
type qrCard struct {
	Lead     string // above the code: "Something wrong here?"
	Title    string
	Subtitle string
	CTA      string // below: "Scan to report a problem"
	URL      string
	SVG      template.HTML
}

// QR sheets are for places, items or supplies; it decides what the page
// says about who can use them.
const (
	qrPlaces   = "places"
	qrItems    = "items"
	qrSupplies = "supplies"
)

func (s *Server) newQRCard(r *http.Request, title, subtitle, path string) (qrCard, error) {
	c := qrCard{Lead: "Something wrong here?", Title: title, Subtitle: subtitle, CTA: "Scan to report a problem", URL: s.absURL(r, path)}
	var err error
	c.SVG, err = qrSVG(c.URL)
	return c, err
}

// renderQRSheet prints cards. Item stickers are small, to fit on the
// things themselves.
func (s *Server) renderQRSheet(w http.ResponseWriter, r *http.Request, kind, title, back string, cards []qrCard) {
	s.render(w, r, http.StatusOK, "qr", map[string]any{
		"Title": title, "Kind": kind, "Cards": cards, "Back": back, "SiteURLSet": s.SiteURL() != "", "Small": kind == qrItems,
	})
}

// itemCards makes report cards for items: one per unit for those tracked
// one by one (with the ID on its sticker, which finds it wherever it's
// moved), and one for the whole item otherwise.
func (s *Server) itemCards(r *http.Request, items []store.Item) ([]qrCard, error) {
	var cards []qrCard
	for _, it := range items {
		where := it.Location()
		if it.Portable {
			where = "" // it won't stay there
		}
		var units []store.Unit
		if !it.Counted {
			var err error
			if units, err = s.store.ItemUnits(it.ID, it.Quantity, s.todayTime()); err != nil {
				return nil, err
			}
		}
		// A single fixed item without an ID of its own is just the item.
		if len(units) == 1 && !it.Portable && !units[0].Tagged() {
			units = nil
		}
		if units == nil {
			c, err := s.newQRCard(r, it.Name, where, fmt.Sprintf("/report?item=%d", it.ID))
			if err != nil {
				return nil, err
			}
			c.Lead = "Something wrong with this?"
			cards = append(cards, c)
			continue
		}
		for _, u := range units {
			c, err := s.newQRCard(r, it.Name, "ID "+u.ID(), fmt.Sprintf("/report?item=%d&unit=%s", it.ID, url.QueryEscape(u.ID())))
			if err != nil {
				return nil, err
			}
			c.Lead = "Something wrong with this?"
			cards = append(cards, c)
		}
	}
	return cards, nil
}

// handleItemQR prints stickers for an item, or each of its units.
func (s *Server) handleItemQR(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cards, err := s.itemCards(r, []store.Item{*item})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderQRSheet(w, r, qrItems, "QR codes: "+item.Name, fmt.Sprintf("/items/%d", item.ID), cards)
}

// handleProductQR prints stickers for every one of a product, everywhere.
func (s *Server) handleProductQR(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProduct(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	items, err := s.store.ListItems(store.ItemFilter{ProductID: p.ID})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cards, err := s.itemCards(r, items)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderQRSheet(w, r, qrItems, "QR codes: "+p.Name, fmt.Sprintf("/products/%d", p.ID), cards)
}

// supplyCards makes cards that open supplies' pages, to take some, recount,
// restock or ask for more.
func (s *Server) supplyCards(r *http.Request, supplies []store.Supply) ([]qrCard, error) {
	cards := make([]qrCard, len(supplies))
	for k, sp := range supplies {
		c, err := s.newQRCard(r, sp.Name, sp.Location(), fmt.Sprintf("/supplies/%d", sp.ID))
		if err != nil {
			return nil, err
		}
		c.Lead, c.CTA = "Took some? Running low?", "Scan to update the count or ask for more"
		cards[k] = c
	}
	return cards, nil
}

// handleSupplyQR prints a card for a supply's shelf or bin.
func (s *Server) handleSupplyQR(w http.ResponseWriter, r *http.Request) {
	sp, err := s.store.GetSupply(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cards, err := s.supplyCards(r, []store.Supply{*sp})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderQRSheet(w, r, qrSupplies, "QR code: "+sp.Name, fmt.Sprintf("/supplies/%d", sp.ID), cards)
}

// handleSuppliesQR prints cards for the supplies listed, filtered as on
// the Supplies page.
func (s *Server) handleSuppliesQR(w http.ResponseWriter, r *http.Request) {
	f := supplyFilter(r)
	supplies, err := s.store.ListSupplies(f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cards, err := s.supplyCards(r, supplies)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderQRSheet(w, r, qrSupplies, "QR codes: supplies", "/supplies?"+r.URL.RawQuery, cards)
}

// handlePlaceQR prints a card that opens "Report a problem" for a place,
// and with ?inside=1 one for everything inside it too, ready to cut out.
func (s *Server) handlePlaceQR(w http.ResponseWriter, r *http.Request) {
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p, ok := tree.Get(pathID(r))
	if !ok {
		s.notFound(w, r)
		return
	}
	places := []store.Place{p}
	title := "QR code: " + p.Label()
	if r.URL.Query().Get("inside") == "1" {
		places = append(places, tree.Inside(p.ID)...)
		title = "QR codes: " + p.Label()
	}
	var cards []qrCard
	for _, pl := range places {
		// The subtitle says where it is: the places it's inside, or what
		// kind of place it is when it's at the top.
		where := pl.KindLabel()
		if parent := tree.Path(pl.ParentID); parent != "" && pl.ParentID != 0 {
			if site, single := tree.SingleSite(); !single || site.ID != pl.ParentID {
				where = parent
			}
		}
		card, err := s.newQRCard(r, pl.Label(), where, fmt.Sprintf("/report?place=%d", pl.ID))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		cards = append(cards, card)
	}
	s.renderQRSheet(w, r, qrPlaces, title, fmt.Sprintf("/places/%d", p.ID), cards)
}
