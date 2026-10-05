package server

import (
	"net/http"
)

// API tokens ---------------------------------------------------------------

func (s *Server) renderTokens(w http.ResponseWriter, r *http.Request, status int, name, newToken string, errs []string) {
	tokens, err := s.store.ListAPITokens()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "tokens/index", map[string]any{
		"Title": "API tokens", "Tokens": tokens, "Name": name, "NewToken": newToken, "APIBase": s.absURL(r, "/api/v1"), "Errors": errs,
	})
}

func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	s.renderTokens(w, r, http.StatusOK, "", "", nil)
}

// handleTokenCreate makes a token and shows it on the page, the one time
// it can be seen; it isn't put in a flash message or redirect.
func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	name := formStr(r, "name")
	var errs []string
	if name == "" {
		errs = append(errs, "Name what will use it, like Event planner.")
	}
	errs = append(errs, tooLong("Name", name, 100)...)
	if errs != nil {
		s.renderTokens(w, r, http.StatusUnprocessableEntity, name, "", errs)
		return
	}
	token, err := s.store.CreateAPIToken(name, currentUser(r).ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderTokens(w, r, http.StatusOK, "", token, nil)
}

func (s *Server) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAPIToken(pathID(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/admin/tokens", "Token revoked. Anything using it can no longer read from the API.")
}
