package server

import (
	"fmt"
	"html/template"
	"net/http"
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

// qrCard is one printable "scan to report a problem" card.
type qrCard struct {
	Title    string
	Subtitle string
	URL      string
	SVG      template.HTML
}

func (s *Server) newQRCard(r *http.Request, title, subtitle, path string) (qrCard, error) {
	c := qrCard{Title: title, Subtitle: subtitle, URL: s.absURL(r, path)}
	var err error
	c.SVG, err = qrSVG(c.URL)
	return c, err
}

func (s *Server) renderQRSheet(w http.ResponseWriter, r *http.Request, title, back string, cards []qrCard) {
	s.render(w, r, http.StatusOK, "qr", map[string]any{
		"Title": title, "Cards": cards, "Back": back, "SiteURLSet": s.SiteURL() != "",
	})
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
	s.renderQRSheet(w, r, title, fmt.Sprintf("/places/%d", p.ID), cards)
}
