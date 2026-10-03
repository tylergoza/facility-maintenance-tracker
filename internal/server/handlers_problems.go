package server

import (
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// reportAccess lets anyone report a problem when an admin has turned on
// public reports, and otherwise requires signing in.
func (s *Server) reportAccess(next http.HandlerFunc) http.Handler {
	signedIn := s.requireUser(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r) == nil && s.PublicReports() {
			w.Header().Set("Cache-Control", "no-store")
			next(w, r)
			return
		}
		signedIn.ServeHTTP(w, r)
	})
}

// absURL turns a path into a full URL on this site, for showing to admins.
func (s *Server) absURL(r *http.Request, path string) string {
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}

// Reporting ----------------------------------------------------------------

func (s *Server) renderReportForm(w http.ResponseWriter, r *http.Request, status int, p store.Problem, errs []string) {
	buildings, err := s.store.ListBuildings()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rooms, err := s.store.ListRooms(0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "problems/report", map[string]any{
		"Title": "Report a problem", "Form": p, "Buildings": buildings, "Rooms": rooms, "Errors": errs,
	})
}

// problemFromForm reads what and where. Reporter fields are only read for
// people who aren't signed in.
func (s *Server) problemFromForm(r *http.Request, p *store.Problem, reporter bool) []string {
	p.BuildingID, p.RoomID = formInt(r, "building_id"), formInt(r, "room_id")
	p.Title, p.Details = formStr(r, "title"), formStr(r, "details")
	var errs []string
	if _, err := s.store.GetBuilding(p.BuildingID); err != nil {
		errs = append(errs, "Choose a building.")
	}
	if p.Title == "" {
		errs = append(errs, "Say briefly what's wrong.")
	}
	errs = append(errs, tooLong("What's wrong", p.Title, 200)...)
	errs = append(errs, tooLong("Details", p.Details, 5000)...)
	if reporter {
		p.ReporterName, p.ReporterContact = formStr(r, "reporter_name"), formStr(r, "reporter_contact")
		errs = append(errs, tooLong("Name", p.ReporterName, 100)...)
		errs = append(errs, tooLong("Contact", p.ReporterContact, 200)...)
	}
	if p.RoomID != 0 {
		room, err := s.store.GetRoom(p.RoomID)
		if err != nil || room.BuildingID != p.BuildingID {
			errs = append(errs, "The selected room is not in the selected building.")
		}
	}
	if p.ItemID != 0 {
		item, err := s.store.GetItem(p.ItemID)
		if err != nil || item.BuildingID != p.BuildingID {
			errs = append(errs, "The item is not in the selected building.")
		} else {
			p.ItemName = item.Name
		}
	}
	return errs
}

func tooLong(field, v string, limit int) []string {
	if utf8.RuneCountInString(v) > limit {
		return []string{fmt.Sprintf("%s must be %d characters or fewer.", field, limit)}
	}
	return nil
}

func (s *Server) handleReportForm(w http.ResponseWriter, r *http.Request) {
	p := store.Problem{BuildingID: queryInt(r, "building"), RoomID: queryInt(r, "room")}
	if room, err := s.store.GetRoom(p.RoomID); err == nil {
		p.BuildingID = room.BuildingID
	}
	// Item links are only on signed-in pages, so only they can preset one.
	if currentUser(r) != nil {
		if item, err := s.store.GetItem(queryInt(r, "item")); err == nil {
			p.ItemID, p.ItemName, p.BuildingID, p.RoomID = item.ID, item.Name, item.BuildingID, item.RoomID
		}
	}
	s.renderReportForm(w, r, http.StatusOK, p, nil)
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var p store.Problem
	if user != nil {
		p.ItemID = formInt(r, "item_id")
	}
	errs := s.problemFromForm(r, &p, user == nil)
	if user != nil {
		p.ReportedBy, p.ReporterName = user.ID, user.Name()
	} else {
		// Bots fill in every field; people never see this one.
		if formStr(r, "website") != "" {
			s.redirect(w, r, "/report", "Thanks! Your report was sent.")
			return
		}
		if p.ReporterName == "" {
			errs = append(errs, "Enter your name so we can follow up.")
		}
		if ip := s.clientIP(r); !s.reportLimiter.allow(ip) {
			s.renderReportForm(w, r, http.StatusTooManyRequests, p, []string{"Too many reports from this device. Please try again later."})
			return
		}
	}
	if errs != nil {
		s.renderReportForm(w, r, http.StatusUnprocessableEntity, p, errs)
		return
	}
	if err := s.store.CreateProblem(&p); err != nil {
		s.serverError(w, r, err)
		return
	}
	if user == nil {
		s.reportLimiter.fail(s.clientIP(r))
		s.redirect(w, r, "/report", "Thanks! Your report was sent to the facilities team.")
		return
	}
	s.redirect(w, r, fmt.Sprintf("/problems/%d", p.ID), "Problem reported.")
}

// Problems -----------------------------------------------------------------

func (s *Server) handleProblems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ProblemFilter{BuildingID: queryInt(r, "building"), Status: q.Get("status")}
	if f.Status == "" {
		f.Status = "active"
	} else if f.Status == "all" {
		f.Status = ""
	}
	mine := q.Get("mine") == "1"
	if mine {
		f.AssignedTo = currentUser(r).ID
	}
	problems, err := s.store.ListProblems(f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	buildings, err := s.store.ListBuildings()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "problems/index", map[string]any{
		"Title": "Problems", "Problems": problems, "Buildings": buildings, "Filter": f, "StatusParam": q.Get("status"), "Mine": mine,
	})
}

func (s *Server) handleProblemShow(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProblem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	updates, err := s.store.ListProblemUpdates(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	users, err := s.store.ListUsers()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "problems/show", map[string]any{
		"Title": p.Title, "Problem": p, "Updates": updates, "Users": users, "Statuses": store.ProblemStatuses,
	})
}

func (s *Server) renderProblemForm(w http.ResponseWriter, r *http.Request, status int, p store.Problem, errs []string) {
	buildings, err := s.store.ListBuildings()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rooms, err := s.store.ListRooms(0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "problems/form", map[string]any{
		"Title": "Edit problem", "Form": p, "Buildings": buildings, "Rooms": rooms, "Errors": errs,
	})
}

func (s *Server) handleProblemEdit(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProblem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderProblemForm(w, r, http.StatusOK, *p, nil)
}

func (s *Server) handleProblemSave(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProblem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if r.PostFormValue("unlink_item") == "1" {
		p.ItemID, p.ItemName = 0, ""
	}
	if errs := s.problemFromForm(r, p, true); errs != nil {
		s.renderProblemForm(w, r, http.StatusUnprocessableEntity, *p, errs)
		return
	}
	if err := s.store.SaveProblemDetails(p); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/problems/%d", p.ID), "Problem updated.")
}

// handleProblemUpdate changes status and/or who's on it and adds a note.
// The "I'm on it" button posts here too.
func (s *Server) handleProblemUpdate(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProblem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	to := fmt.Sprintf("/problems/%d", p.ID)
	c := store.ProblemChange{ProblemID: p.ID, Status: formStr(r, "status"), AssignedTo: formInt(r, "assigned_to"),
		Note: formStr(r, "note"), UserID: currentUser(r).ID}
	switch c.Status {
	case store.ProblemOpen, store.ProblemInProgress, store.ProblemResolved:
	default:
		s.setFlash(w, r, "error", "Choose a status.")
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	if c.AssignedTo != 0 {
		if _, err := s.store.GetUser(c.AssignedTo); err != nil {
			s.setFlash(w, r, "error", "That person no longer has an account.")
			http.Redirect(w, r, to, http.StatusSeeOther)
			return
		}
	}
	if msg := tooLong("Note", c.Note, 5000); msg != nil {
		s.setFlash(w, r, "error", msg[0])
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	changed, err := s.store.UpdateProblem(c)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !changed {
		s.setFlash(w, r, "info", "Nothing changed.")
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	msg := "Problem updated."
	switch {
	case c.Status == store.ProblemResolved && p.Status != store.ProblemResolved:
		msg = "Marked resolved."
	case c.AssignedTo == currentUser(r).ID && p.AssignedTo != c.AssignedTo:
		msg = "It's yours. Thanks for taking it on."
	}
	s.redirect(w, r, to, msg)
}

func (s *Server) handleProblemDelete(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProblem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.DeleteProblem(p.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/problems", "Problem deleted.")
}
