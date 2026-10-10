package server

import (
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// reportAccess lets anyone report a problem when an admin has turned on
// public reports for what it's about (an item, or just a place), and
// otherwise requires signing in.
func (s *Server) reportAccess(next http.HandlerFunc) http.Handler {
	signedIn := s.requireUser(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aboutItem := r.URL.Query().Get("item") != ""
		if r.Method == http.MethodPost {
			aboutItem = r.PostFormValue("item_id") != ""
		}
		public := s.PublicReports()
		if aboutItem {
			public = s.PublicItemReports()
		}
		if currentUser(r) == nil && public {
			w.Header().Set("Cache-Control", "no-store")
			next(w, r)
			return
		}
		signedIn.ServeHTTP(w, r)
	})
}

// absURL turns a path into a full URL on this site, for links and QR
// codes used outside the app. It uses the site address from Settings when
// set; otherwise it's worked out from the request.
func (s *Server) absURL(r *http.Request, path string) string {
	if base := s.SiteURL(); base != "" {
		return base + path
	}
	return s.guessedURL(r) + path
}

// Reporting ----------------------------------------------------------------

// problemFormData loads what the report and edit forms pick from: places,
// items (narrowed to the chosen place in the browser), and their units
// (narrowed to the chosen item).
func (s *Server) problemFormData() (map[string]any, error) {
	tree, err := s.store.Places()
	if err != nil {
		return nil, err
	}
	items, err := s.store.ListItems(store.ItemFilter{})
	if err != nil {
		return nil, err
	}
	units, err := s.store.UnitsOf(items)
	if err != nil {
		return nil, err
	}
	options := itemOptions(tree, items)
	for i := range options {
		options[i].Units = units[options[i].ID]
	}
	return map[string]any{"Places": tree.All(), "Items": options}, nil
}

// renderReportForm shows the report form. fixed means it came from a
// link or QR code for one item, which is shown instead of the place and
// item pickers. A unit's code names the unit too; otherwise the item's
// units are offered to pick from.
func (s *Server) renderReportForm(w http.ResponseWriter, r *http.Request, status int, p store.Problem, fixed bool, errs []string) {
	data, err := s.problemFormData()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	signedIn := currentUser(r) != nil
	data["Title"], data["Form"], data["Errors"] = "Report a problem", p, errs
	data["ItemsOpen"] = signedIn || s.PublicItemReports()
	data["PlacesOpen"] = signedIn || s.PublicReports()
	if item, err := s.store.GetItem(p.ItemID); fixed && err == nil {
		data["Fixed"] = item
		units, err := s.itemUnits(item)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if p.Unit >= 1 && p.Unit <= len(units) && r.Method == http.MethodGet {
			data["FixedUnit"] = units[p.Unit-1]
		} else {
			data["FixedUnits"] = units
		}
	}
	s.render(w, r, status, "problems/report", data)
}

// problemFromForm reads what and where. Reporter fields are only read for
// people who aren't signed in.
func (s *Server) problemFromForm(r *http.Request, p *store.Problem, reporter bool) []string {
	p.PlaceID, p.ItemID, p.Unit = formInt(r, "place_id"), formInt(r, "item_id"), 0
	p.Title, p.Details = formStr(r, "title"), formStr(r, "details")
	var errs []string
	tree, err := s.store.Places()
	if err != nil {
		return []string{"Couldn't check the place. Please try again."}
	}
	if _, ok := tree.Get(p.PlaceID); !ok {
		errs = append(errs, "Choose where it is.")
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
	// Naming the item pins down the place: the problem goes where it is.
	if p.ItemID != 0 {
		item, err := s.store.GetItem(p.ItemID)
		switch {
		case err != nil:
			errs = append(errs, "That item no longer exists.")
		case !tree.Within(item.PlaceID, p.PlaceID):
			errs = append(errs, "That item isn't in the place you chose.")
		default:
			p.ItemName, p.PlaceID = item.Name, item.PlaceID
			// Which one, picked or from a unit's QR code; dropped if it's
			// no longer one. Not saying means the group, or not sure which.
			if u := int(formInt(r, "unit")); item.HasUnits() && u >= 1 && u <= item.Quantity {
				p.Unit = u
			}
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
	p := store.Problem{PlaceID: queryInt(r, "place")}
	// QR codes printed before places used ?building= and ?room=.
	if id, err := s.store.LegacyPlace(queryInt(r, "building"), queryInt(r, "room")); err == nil {
		p.PlaceID = id
	}
	// An item's QR code names it, and a unit's adds the ID on its sticker.
	// The unit may have moved to another item since it was printed.
	itemID := queryInt(r, "item")
	if tag := r.URL.Query().Get("unit"); tag != "" {
		if id, n, err := s.store.FindUnit(itemID, tag); err == nil {
			itemID, p.Unit = id, n
		}
	}
	fixed := false
	if item, err := s.store.GetItem(itemID); err == nil {
		p.ItemID, p.ItemName, p.PlaceID, fixed = item.ID, item.Name, item.PlaceID, true
		if !item.HasUnits() {
			p.Unit = 0
		}
	}
	s.renderReportForm(w, r, http.StatusOK, p, fixed, nil)
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var p store.Problem
	errs := s.problemFromForm(r, &p, user == nil)
	fixed := r.PostFormValue("fixed") == "1" && p.ItemID != 0
	// Where people who aren't signed in land afterwards: somewhere they're
	// allowed, ready for another report.
	again := "/report"
	if p.ItemID != 0 {
		again = fmt.Sprintf("/report?item=%d", p.ItemID)
	}
	if user != nil {
		p.ReportedBy, p.ReporterName = user.ID, user.Name()
	} else {
		// Bots fill in every field; people never see this one.
		if formStr(r, "website") != "" {
			s.redirect(w, r, again, "Thanks! Your report was sent.")
			return
		}
		if p.ReporterName == "" {
			errs = append(errs, "Enter your name so we can follow up.")
		}
		if ip := s.clientIP(r); !s.reportLimiter.allow(ip) {
			s.renderReportForm(w, r, http.StatusTooManyRequests, p, fixed, []string{"Too many reports from this device. Please try again later."})
			return
		}
	}
	if errs != nil {
		s.renderReportForm(w, r, http.StatusUnprocessableEntity, p, fixed, errs)
		return
	}
	if err := s.store.CreateProblem(&p); err != nil {
		s.serverError(w, r, err)
		return
	}
	if user == nil {
		s.reportLimiter.fail(s.clientIP(r))
		s.redirect(w, r, again, "Thanks! Your report was sent to the facilities team.")
		return
	}
	s.redirect(w, r, fmt.Sprintf("/problems/%d", p.ID), "Problem reported.")
}

// Problems -----------------------------------------------------------------

func (s *Server) handleProblems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ProblemFilter{PlaceID: queryInt(r, "place"), Tag: q.Get("tag"), Status: q.Get("status")}
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
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "problems/index", map[string]any{
		"Title": "Problems", "Problems": problems, "Places": tree.All(), "Tags": tree.Tags(), "Filter": f, "StatusParam": q.Get("status"), "Mine": mine, "Live": true,
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
	// Whoever's on it stays listed even if their access was removed.
	users, err := s.pickableUsers(r, []int64{p.AssignedTo})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := map[string]any{"Title": p.Title, "Problem": p, "Updates": updates, "Users": users, "Statuses": store.ProblemStatuses, "Live": true}
	if p.ItemID != 0 {
		item, err := s.store.GetItem(p.ItemID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		units, err := s.itemUnits(item)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data["Item"], data["Units"] = item, units
	}
	s.render(w, r, http.StatusOK, "problems/show", data)
}

func (s *Server) renderProblemForm(w http.ResponseWriter, r *http.Request, status int, p store.Problem, errs []string) {
	data, err := s.problemFormData()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data["Title"], data["Form"], data["Errors"] = "Edit problem", p, errs
	s.render(w, r, status, "problems/form", data)
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
	if errs := s.problemFromForm(r, p, true); errs != nil {
		s.renderProblemForm(w, r, http.StatusUnprocessableEntity, *p, errs)
		return
	}
	if err := s.store.SaveProblemDetails(p); err != nil {
		s.serverError(w, r, err)
		return
	}
	if p.ItemID != 0 {
		if err := s.store.SetProblemUnit(p.ID, p.Unit, currentUser(r).ID); err != nil {
			s.serverError(w, r, err)
			return
		}
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
	noAccess := func() {
		s.setFlash(w, r, "error", "That person no longer has access to this app, so they can't be put on it. Choose someone else.")
		http.Redirect(w, r, to, http.StatusSeeOther)
	}
	if c.AssignedTo != 0 {
		u, err := s.store.GetUser(c.AssignedTo)
		if err != nil {
			s.setFlash(w, r, "error", "That person no longer has an account.")
			http.Redirect(w, r, to, http.StatusSeeOther)
			return
		}
		// Someone whose access was removed stays on it if they already
		// were, but can't be newly put on it.
		if !u.Active && c.AssignedTo != p.AssignedTo {
			noAccess()
			return
		}
	}
	if msg := tooLong("Note", c.Note, 5000); msg != nil {
		s.setFlash(w, r, "error", msg[0])
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	changed, err := s.store.UpdateProblem(c)
	if errors.Is(err, store.ErrNotFound) && c.AssignedTo != 0 && c.AssignedTo != p.AssignedTo {
		// Their access was removed since the check above.
		noAccess()
		return
	}
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

// handleProblemUnit records which of the item's units the problem is about.
func (s *Server) handleProblemUnit(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProblem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	to := fmt.Sprintf("/problems/%d", p.ID)
	unit := int(formInt(r, "unit"))
	if p.ItemID == 0 || unit < 0 || unit > p.ItemQuantity {
		s.setFlash(w, r, "error", "Choose one of the item's numbers.")
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	if err := s.store.SetProblemUnit(p.ID, unit, currentUser(r).ID); errors.Is(err, store.ErrCounted) {
		s.setFlash(w, r, "error", p.ItemName+" are only counted, so they aren't numbered.")
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := "Cleared which one."
	if unit != 0 {
		msg = fmt.Sprintf("Noted: it's #%d.", unit)
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
