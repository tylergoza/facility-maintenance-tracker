package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// Supplies ---------------------------------------------------------------

// supplyFilter reads the Supplies page's filters.
func supplyFilter(r *http.Request) store.SupplyFilter {
	q := r.URL.Query()
	return store.SupplyFilter{PlaceID: queryInt(r, "place"), Tag: q.Get("tag"), Query: q.Get("q"), LowOnly: q.Get("low") == "1"}
}

func (s *Server) handleSupplies(w http.ResponseWriter, r *http.Request) {
	f := supplyFilter(r)
	supplies, err := s.store.ListSupplies(f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "supplies/index", map[string]any{
		"Title": "Supplies", "Supplies": supplies, "Places": tree.All(), "Tags": tree.Tags(), "Filter": f, "Query": r.URL.RawQuery, "Live": true,
	})
}

func (s *Server) supplyForm(w http.ResponseWriter, r *http.Request, status int, title string, sp store.Supply, errs []string) {
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "supplies/form", map[string]any{
		"Title": title, "Form": sp, "Places": tree.All(), "Errors": errs,
	})
}

// formCount parses a non-negative whole number; blank counts as 0.
func formCount(r *http.Request, key string) (int, bool) {
	v := formStr(r, key)
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 0
}

func (s *Server) supplyFromForm(r *http.Request, sp *store.Supply) []string {
	sp.PlaceID = formInt(r, "place_id")
	sp.Name, sp.Unit, sp.Notes = formStr(r, "name"), formStr(r, "unit"), formStr(r, "notes")
	sp.Reusable = r.PostFormValue("reusable") == "1"
	var errs []string
	if _, err := s.store.GetPlace(sp.PlaceID); err != nil {
		errs = append(errs, "Choose where it's kept.")
	}
	if sp.Name == "" {
		errs = append(errs, "Name is required.")
	}
	var ok bool
	if sp.ReorderAt, ok = formCount(r, "reorder_at"); !ok {
		errs = append(errs, "Reorder level must be a whole number, 0 or more.")
	}
	if sp.ID == 0 {
		if sp.Quantity, ok = formCount(r, "quantity"); !ok {
			errs = append(errs, "Quantity on hand must be a whole number, 0 or more.")
		}
		if sp.Reusable {
			if sp.InUse, ok = formCount(r, "in_use"); !ok {
				errs = append(errs, "In use must be a whole number, 0 or more.")
			}
			if sp.Cleaning, ok = formCount(r, "cleaning"); !ok {
				errs = append(errs, "Out for cleaning must be a whole number, 0 or more.")
			}
		}
	}
	return errs
}

func (s *Server) handleSupplyNew(w http.ResponseWriter, r *http.Request) {
	sp := store.Supply{PlaceID: queryInt(r, "place")}
	s.supplyForm(w, r, http.StatusOK, "Add supply", sp, nil)
}

func (s *Server) handleSupplyCreate(w http.ResponseWriter, r *http.Request) {
	var sp store.Supply
	if errs := s.supplyFromForm(r, &sp); errs != nil {
		s.supplyForm(w, r, http.StatusUnprocessableEntity, "Add supply", sp, errs)
		return
	}
	if err := s.store.SaveSupply(&sp, currentUser(r).ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/supplies/%d", sp.ID), sp.Name+" added.")
}

func (s *Server) handleSupplyShow(w http.ResponseWriter, r *http.Request) {
	sp, err := s.store.GetSupply(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	changes, err := s.store.ListSupplyChanges(sp.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tasks, err := s.store.ListTasks(store.TaskFilter{SupplyID: sp.ID, ActiveOnly: true})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	items, err := s.store.ListItems(store.ItemFilter{SupplyID: sp.ID})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "supplies/show", map[string]any{"Title": sp.Name, "Supply": sp, "Changes": changes, "Tasks": tasks, "Items": items, "Live": true})
}

func (s *Server) handleSupplyEdit(w http.ResponseWriter, r *http.Request) {
	sp, err := s.store.GetSupply(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.supplyForm(w, r, http.StatusOK, "Edit "+sp.Name, *sp, nil)
}

func (s *Server) handleSupplyUpdate(w http.ResponseWriter, r *http.Request) {
	sp, err := s.store.GetSupply(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if errs := s.supplyFromForm(r, sp); errs != nil {
		s.supplyForm(w, r, http.StatusUnprocessableEntity, "Edit supply", *sp, errs)
		return
	}
	err = s.store.SaveSupply(sp, currentUser(r).ID)
	if errors.Is(err, store.ErrReusableInCirculation) {
		s.supplyForm(w, r, http.StatusUnprocessableEntity, "Edit supply", *sp, []string{
			"Some are still in use or out for cleaning. Record them as back from cleaning or thrown out before making this a used-up supply.",
		})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/supplies/%d", sp.ID), "Supply updated.")
}

func (s *Server) handleSupplyDelete(w http.ResponseWriter, r *http.Request) {
	sp, err := s.store.GetSupply(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.DeleteSupply(sp.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/places/%d", sp.PlaceID), sp.Name+" deleted.")
}

// handleSupplyAdjust records a change to a supply's counts: used,
// restocked, recounted or thrown out, and for reusables put in use, sent
// for cleaning, swapped or returned. It is posted from the supply page and
// from the quick buttons in lists, and returns to "next" when given.
func (s *Server) handleSupplyAdjust(w http.ResponseWriter, r *http.Request) {
	sp, err := s.store.GetSupply(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	next := r.PostFormValue("next")
	if next == "" {
		next = fmt.Sprintf("/supplies/%d", sp.ID)
	}
	next = safeRedirect(next)
	fail := func(msg string) {
		s.setFlash(w, r, "error", msg)
		http.Redirect(w, r, next, http.StatusSeeOther)
	}

	a := store.Adjustment{SupplyID: sp.ID, Kind: formStr(r, "kind"), From: formStr(r, "from"), Note: formStr(r, "note"), UserID: currentUser(r).ID}
	switch {
	case a.Kind == "used" || a.Kind == "restocked" || a.Kind == "counted" || a.Kind == "retired":
	case store.ReusableKinds[a.Kind] && sp.Reusable:
	default:
		fail("Choose what happened.")
		return
	}
	amount, err := strconv.Atoi(formStr(r, "amount"))
	// Everything needs at least 1, except a recount which may be 0.
	if err != nil || amount < 0 || (amount == 0 && a.Kind != "counted") {
		fail("Enter a whole number for the amount.")
		return
	}
	a.Amount = amount

	var short *store.NotEnoughError
	if err := s.store.AdjustSupply(a); errors.As(err, &short) {
		fail(fmt.Sprintf("Can't do that: %s has %s.", sp.Name, short.Error()))
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	if sp, err = s.store.GetSupply(sp.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := fmt.Sprintf("%s: %s.", sp.Name, sp.StockSummary())
	if !sp.Reusable {
		msg = fmt.Sprintf("%s: %s on hand.", sp.Name, sp.QuantityLabel())
	}
	switch {
	case sp.Stock() == "out" && sp.Reusable:
		msg += " No clean ones left."
	case sp.Stock() == "out":
		msg = sp.Name + " is now out of stock."
	case sp.Stock() == "low" && sp.Reusable:
		msg += " Running low on clean ones."
	case sp.Stock() == "low":
		msg += " Time to reorder."
	}
	s.redirect(w, r, next, msg)
}

// handleSupplyRequest asks for more of a supply, often right after
// scanning its QR code.
func (s *Server) handleSupplyRequest(w http.ResponseWriter, r *http.Request) {
	sp, err := s.store.GetSupply(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	to := fmt.Sprintf("/supplies/%d", sp.ID)
	note := formStr(r, "note")
	if msg := tooLong("Note", note, 200); msg != nil {
		s.setFlash(w, r, "error", msg[0])
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	if err := s.store.RequestSupply(sp.ID, currentUser(r).ID, note); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, to, "Asked for more "+sp.Name+". It's flagged on the dashboard until it's restocked.")
}

func (s *Server) handleSupplyRequestCancel(w http.ResponseWriter, r *http.Request) {
	sp, err := s.store.GetSupply(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.CancelSupplyRequest(sp.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/supplies/%d", sp.ID), "Request for more "+sp.Name+" cleared.")
}
