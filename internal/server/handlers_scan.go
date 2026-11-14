package server

import (
	"encoding/json"
	"net/http"
	"net/url"
)

// handleScan reads QR codes with the camera (see scan_controller.js).
// Codes printed with the site address, or with an earlier one, are accepted
// however the site is reached now.
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	origins := []string{}
	for _, site := range append([]string{s.SiteURL()}, s.cfg.OldSiteURLs...) {
		if u, err := url.Parse(site); err == nil && u.Host != "" {
			origins = append(origins, u.Scheme+"://"+u.Host)
		}
	}
	b, _ := json.Marshal(origins)
	s.render(w, r, http.StatusOK, "scan", map[string]any{"Title": "Scan", "Origins": string(b)})
}
