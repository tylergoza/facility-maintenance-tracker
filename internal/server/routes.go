package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"time"
)

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Static assets & PWA plumbing
	mux.Handle("GET /static/", s.staticHandler())
	mux.HandleFunc("GET /sw.js", s.handleServiceWorker)
	mux.HandleFunc("GET /manifest.webmanifest", s.handleManifest)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.asset("icons/icon.svg"), http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /offline", s.handleOffline)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.DB.PingContext(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})

	// Public
	mux.HandleFunc("GET /{$}", s.handleDashboard)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetup)

	// Public when an admin has turned on public reports; otherwise signed-in only.
	mux.Handle("GET /report", s.reportAccess(s.handleReportForm))
	mux.Handle("POST /report", s.reportAccess(s.handleReport))

	// Signed-in editors
	auth := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireUser(h)) }

	auth("GET /places", s.handlePlaces)
	auth("GET /places/new", s.handlePlaceNew)
	auth("POST /places", s.handlePlaceCreate)
	auth("GET /places/bulk", s.handlePlaceBulkNew)
	auth("POST /places/bulk", s.handlePlaceBulkCreate)
	auth("GET /places/{id}", s.handlePlaceShow)
	auth("GET /places/{id}/edit", s.handlePlaceEdit)
	auth("POST /places/{id}", s.handlePlaceUpdate)
	auth("POST /places/{id}/delete", s.handlePlaceDelete)
	auth("GET /places/{id}/qr", s.handlePlaceQR)

	// Pages from before places replaced buildings and rooms.
	auth("GET /buildings", s.handleLegacy(false))
	auth("GET /buildings/{id}", s.handleLegacy(false))
	auth("GET /buildings/{id}/qr", s.handleLegacy(false))
	auth("GET /rooms/{id}", s.handleLegacy(true))
	auth("GET /rooms/{id}/qr", s.handleLegacy(true))

	auth("GET /items", s.handleItems)
	auth("GET /items/new", s.handleItemNew)
	auth("POST /items", s.handleItemCreate)
	auth("GET /items/{id}", s.handleItemShow)
	auth("GET /items/{id}/edit", s.handleItemEdit)
	auth("POST /items/{id}", s.handleItemUpdate)
	auth("POST /items/{id}/delete", s.handleItemDelete)
	auth("GET /items/{id}/log", s.handleLogNew)
	auth("POST /items/{id}/log", s.handleLogCreate)
	auth("GET /items/{id}/replace", s.handleReplaceForm)
	auth("POST /items/{id}/replace", s.handleReplace)
	auth("GET /items/{id}/move", s.handleMoveForm)
	auth("POST /items/{id}/move", s.handleMove)
	auth("GET /items/{id}/units", s.handleUnitsForm)
	auth("POST /items/{id}/units", s.handleUnitsSave)

	auth("GET /supplies", s.handleSupplies)
	auth("GET /supplies/new", s.handleSupplyNew)
	auth("POST /supplies", s.handleSupplyCreate)
	auth("GET /supplies/{id}", s.handleSupplyShow)
	auth("GET /supplies/{id}/edit", s.handleSupplyEdit)
	auth("POST /supplies/{id}", s.handleSupplyUpdate)
	auth("POST /supplies/{id}/delete", s.handleSupplyDelete)
	auth("POST /supplies/{id}/adjust", s.handleSupplyAdjust)

	auth("GET /problems", s.handleProblems)
	auth("GET /problems/{id}", s.handleProblemShow)
	auth("GET /problems/{id}/edit", s.handleProblemEdit)
	auth("POST /problems/{id}", s.handleProblemSave)
	auth("POST /problems/{id}/update", s.handleProblemUpdate)
	auth("POST /problems/{id}/unit", s.handleProblemUnit)
	auth("POST /problems/{id}/delete", s.handleProblemDelete)

	auth("GET /tasks/new", s.handleTaskNew)
	auth("POST /tasks", s.handleTaskCreate)
	auth("GET /tasks/{id}/edit", s.handleTaskEdit)
	auth("POST /tasks/{id}", s.handleTaskUpdate)
	auth("POST /tasks/{id}/delete", s.handleTaskDelete)
	auth("GET /tasks/{id}/complete", s.handleTaskCompleteForm)
	auth("POST /tasks/{id}/complete", s.handleTaskComplete)

	auth("POST /logs/{id}/delete", s.handleLogDelete)
	auth("GET /history", s.handleHistory)

	auth("GET /account", s.handleAccount)
	auth("POST /account/password", s.handleAccountPassword)

	// Admins
	admin := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireAdmin(h)) }
	admin("GET /admin/users", s.handleUsers)
	admin("GET /admin/users/new", s.handleUserNew)
	admin("POST /admin/users", s.handleUserCreate)
	admin("GET /admin/users/{id}/edit", s.handleUserEdit)
	admin("POST /admin/users/{id}", s.handleUserUpdate)
	admin("POST /admin/users/{id}/delete", s.handleUserDelete)
	admin("GET /admin/settings", s.handleSettings)
	admin("POST /admin/settings", s.handleSettingsUpdate)
	admin("GET /admin/backup", s.handleBackup)

	mux.HandleFunc("/", s.notFound)

	return s.logRequests(s.securityHeaders(s.loadSession(s.csrf(mux))))
}

// Middleware -------------------------------------------------------------

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		defer func() {
			if err := recover(); err != nil {
				s.log.Error("panic", "path", r.URL.Path, "err", err)
				http.Error(rec, "internal error", http.StatusInternalServerError)
			}
			if !strings.HasPrefix(r.URL.Path, "/static/") {
				s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
					"dur", time.Since(start).Round(time.Millisecond), "ip", s.clientIP(r))
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", s.csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if s.isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

const sessionCookie = "mt_session"

func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			if sess, err := s.store.GetSession(c.Value); err == nil {
				r = withValue(r, ctxSession, sess)
				r = withValue(r, ctxUser, &sess.User)
			} else {
				http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
			}
		}
		next.ServeHTTP(w, r)
	})
}

// csrf implements the double-submit cookie pattern: every visitor gets a
// random token cookie and every POST must echo it in the _csrf field (or
// X-CSRF-Token header). Combined with SameSite=Lax cookies this blocks
// cross-site form posts.
const csrfCookie = "mt_csrf"

func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		token := ""
		if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) >= 32 {
			token = c.Value
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == http.MethodPatch {
			sent := r.Header.Get("X-CSRF-Token")
			if sent == "" {
				sent = r.PostFormValue("_csrf")
			}
			if token == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(token)) != 1 {
				s.render(w, r, http.StatusForbidden, "error", map[string]any{
					"Title":   "Form expired",
					"Message": "Your form session expired. Please go back, refresh the page, and try again.",
				})
				return
			}
		}
		if token == "" {
			token = randomToken()
			http.SetCookie(w, &http.Cookie{
				Name: csrfCookie, Value: token, Path: "/", HttpOnly: true,
				SameSite: http.SameSiteLaxMode, Secure: s.isHTTPS(r), MaxAge: 365 * 24 * 3600,
			})
		}
		next.ServeHTTP(w, withValue(r, ctxCSRF, token))
	})
}

func (s *Server) requireUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r) == nil {
			dest := r.URL.RequestURI()
			if r.Method != http.MethodGet {
				dest = "/"
			}
			http.Redirect(w, r, "/login?next="+urlEscape(dest), http.StatusSeeOther)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		next(w, r)
	})
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.Handler {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsAdmin {
			s.render(w, r, http.StatusForbidden, "error", map[string]any{
				"Title": "Admins only", "Message": "You need administrator access for that page.",
			})
			return
		}
		next(w, r)
	})
}

func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
