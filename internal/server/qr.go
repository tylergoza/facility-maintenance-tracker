package server

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"rsc.io/qr"
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

// handleRoomQR prints one card that opens "Report a problem" for a room.
func (s *Server) handleRoomQR(w http.ResponseWriter, r *http.Request) {
	room, err := s.store.GetRoom(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	card, err := s.newQRCard(r, room.Label(), room.BuildingName, fmt.Sprintf("/report?room=%d", room.ID))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderQRSheet(w, r, "QR code: "+room.Label(), fmt.Sprintf("/rooms/%d", room.ID), []qrCard{card})
}

// handleBuildingQR prints a card for the building as a whole plus one for
// every room, ready to cut out.
func (s *Server) handleBuildingQR(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.GetBuilding(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rooms, err := s.store.ListRooms(b.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	card, err := s.newQRCard(r, b.Name, "Anywhere in the building", fmt.Sprintf("/report?building=%d", b.ID))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cards := []qrCard{card}
	for _, room := range rooms {
		card, err := s.newQRCard(r, room.Label(), b.Name, fmt.Sprintf("/report?room=%d", room.ID))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		cards = append(cards, card)
	}
	s.renderQRSheet(w, r, "QR codes: "+b.Name, fmt.Sprintf("/buildings/%d", b.ID), cards)
}
