package server

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// Grouping ---------------------------------------------------------------

type taskGroup struct {
	Status store.Status
	Tasks  []store.Task
}

type taskGroups struct {
	Groups []taskGroup
	Counts map[store.Status]int
	Total  int
}

var groupOrder = []store.Status{store.StatusOverdue, store.StatusDueSoon, store.StatusUpcoming, store.StatusUnscheduled}

// groupTasks buckets active tasks by status. Within each bucket tasks go
// place by place, rooms before the floor or building they're in, keeping
// the incoming due-date order as the tie-breaker.
func (s *Server) groupTasks(tasks []store.Task) taskGroups {
	byStatus := map[store.Status][]store.Task{}
	for _, t := range tasks {
		st := s.status(t.NextDueOn)
		byStatus[st] = append(byStatus[st], t)
	}
	g := taskGroups{Counts: map[store.Status]int{}, Total: len(tasks)}
	for _, st := range groupOrder {
		slices.SortStableFunc(byStatus[st], func(a, b store.Task) int {
			return cmp.Compare(a.Place.PostOrder, b.Place.PostOrder)
		})
		g.Groups = append(g.Groups, taskGroup{Status: st, Tasks: byStatus[st]})
		g.Counts[st] = len(byStatus[st])
	}
	return g
}

type buildingHealth struct {
	ID          int64
	Name        string
	Overdue     int
	DueSoon     int
	Upcoming    int
	Unscheduled int
	Total       int
}

// Worst returns the most urgent status present, for colouring.
func (b buildingHealth) Worst() store.Status {
	switch {
	case b.Overdue > 0:
		return store.StatusOverdue
	case b.DueSoon > 0:
		return store.StatusDueSoon
	case b.Upcoming > 0:
		return store.StatusUpcoming
	}
	return store.StatusUnscheduled
}

// buildingHealth counts tasks by status for each building, in tree order.
// Things outside any building count toward their site, which is listed
// only when it has some.
func (s *Server) buildingHealth(tree *store.Places, tasks []store.Task) []buildingHealth {
	byID := map[int64]*buildingHealth{}
	for _, p := range tree.All() {
		if p.Kind == store.KindBuilding || p.Kind == store.KindSite {
			byID[p.ID] = &buildingHealth{ID: p.ID, Name: p.Label()}
		}
	}
	for _, t := range tasks {
		b, ok := tree.Building(t.PlaceID)
		if !ok {
			continue
		}
		h := byID[b.ID]
		h.Total++
		switch s.status(t.NextDueOn) {
		case store.StatusOverdue:
			h.Overdue++
		case store.StatusDueSoon:
			h.DueSoon++
		case store.StatusUpcoming:
			h.Upcoming++
		default:
			h.Unscheduled++
		}
	}
	var out []buildingHealth
	for _, p := range tree.All() {
		if h := byID[p.ID]; h != nil && (p.Kind == store.KindBuilding || h.Total > 0) {
			out = append(out, *h)
		}
	}
	return out
}

// Dashboard (public) -----------------------------------------------------

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if n, _ := s.store.CountUsers(); n == 0 {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	placeID := queryInt(r, "place")
	selected, ok := tree.Get(placeID)
	if !ok {
		placeID = 0
	}
	tasks, err := s.store.ListTasks(store.TaskFilter{PlaceID: placeID, ActiveOnly: true})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	lowSupplies, err := s.store.ListSupplies(store.SupplyFilter{PlaceID: placeID, LowOnly: true})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Reports can name people and contact details, so only signed-in
	// users see them.
	var problems []store.Problem
	if currentUser(r) != nil {
		if problems, err = s.store.ListProblems(store.ProblemFilter{PlaceID: placeID, Status: "active"}); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	var health []buildingHealth
	if placeID == 0 {
		health = s.buildingHealth(tree, tasks)
	}
	title := "Maintenance dashboard"
	if placeID != 0 {
		title = selected.Path
	}
	s.render(w, r, http.StatusOK, "dashboard", map[string]any{
		"Title": title, "Places": filterPlaces(tree, true), "Selected": selected, "PlaceID": placeID,
		"Tasks": s.groupTasks(tasks), "Health": health, "LowSupplies": lowSupplies, "Problems": problems, "DueSoonDays": s.DueSoonDays(),
		"Updated": time.Now().Format("Mon Jan 2, 3:04 PM"),
	})
}

// Tasks ------------------------------------------------------------------

func (s *Server) taskForm(w http.ResponseWriter, r *http.Request, status int, title string, t store.Task, errs []string) {
	item, err := s.store.GetItem(t.ItemID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Only used-up supplies can be linked; reusables are tracked by swapping.
	all, err := s.store.ListSupplies(store.SupplyFilter{})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var supplies []store.Supply
	for _, sp := range all {
		if !sp.Reusable {
			supplies = append(supplies, sp)
		}
	}
	s.render(w, r, status, "tasks/form", map[string]any{
		"Title": title, "Form": t, "Item": item, "Supplies": supplies, "Errors": errs,
	})
}

// taskFromForm reads the task form. It needs the store to check the supply.
func (s *Server) taskFromForm(r *http.Request, t *store.Task) []string {
	t.Name, t.Description = formStr(r, "name"), formStr(r, "description")
	t.LastCompletedOn, t.NextDueOn = formStr(r, "last_completed_on"), formStr(r, "next_due_on")
	t.IntervalValue, t.IntervalUnit = 0, ""
	if r.PostFormValue("recurring") == "1" {
		t.IntervalValue, _ = strconv.Atoi(formStr(r, "interval_value"))
		t.IntervalUnit = formStr(r, "interval_unit")
	}
	if r.PostFormValue("has_active") == "1" {
		t.Active = r.PostFormValue("active") == "1"
	}
	t.SupplyID, t.SupplyAmount, t.SupplyAlways = 0, 1, true
	var errs []string
	if r.PostFormValue("uses_supply") == "1" {
		t.SupplyID = formInt(r, "supply_id")
		t.SupplyAlways = r.PostFormValue("supply_always") != "0"
		amount, err := strconv.Atoi(formStr(r, "supply_amount"))
		if err != nil || amount < 1 {
			errs = append(errs, "How many of the supply each time must be 1 or more.")
		}
		t.SupplyAmount = max(amount, 1)
		if sp, err := s.store.GetSupply(t.SupplyID); err != nil {
			errs = append(errs, "Choose the supply this task uses.")
		} else if sp.Reusable {
			errs = append(errs, sp.Name+" is reusable; tasks can only use supplies that get used up.")
		}
	}
	if t.Name == "" {
		errs = append(errs, "Task name is required.")
	}
	if r.PostFormValue("recurring") == "1" {
		if t.IntervalValue < 1 || t.IntervalValue > 1000 {
			errs = append(errs, "Repeat interval must be a number from 1 to 1000.")
		}
		switch t.IntervalUnit {
		case "days", "weeks", "months", "years":
		default:
			errs = append(errs, "Choose a repeat unit.")
		}
	}
	if t.LastCompletedOn != "" && !validDate(t.LastCompletedOn) {
		errs = append(errs, "Last completed date is not valid.")
	}
	if t.NextDueOn != "" && !validDate(t.NextDueOn) {
		errs = append(errs, "Next due date is not valid.")
	}
	if len(errs) == 0 && t.NextDueOn == "" && t.LastCompletedOn != "" && t.Recurring() {
		t.NextDueOn, _ = store.AddInterval(t.LastCompletedOn, t.IntervalValue, t.IntervalUnit)
	}
	return errs
}

func (s *Server) handleTaskNew(w http.ResponseWriter, r *http.Request) {
	t := store.Task{ItemID: queryInt(r, "item"), Active: true, IntervalValue: 1, IntervalUnit: "years", SupplyAmount: 1, SupplyAlways: true}
	// Start from the supply the item uses, enough to replace all of them.
	if item, err := s.store.GetItem(t.ItemID); err == nil && item.SupplyID != 0 {
		t.SupplyID, t.SupplyAmount = item.SupplyID, item.SupplyTotal()
	}
	s.taskForm(w, r, http.StatusOK, "Add maintenance task", t, nil)
}

func (s *Server) handleTaskCreate(w http.ResponseWriter, r *http.Request) {
	t := store.Task{ItemID: formInt(r, "item_id"), Active: true}
	if _, err := s.store.GetItem(t.ItemID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if errs := s.taskFromForm(r, &t); errs != nil {
		s.taskForm(w, r, http.StatusUnprocessableEntity, "Add maintenance task", t, errs)
		return
	}
	if err := s.store.SaveTask(&t); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/items/%d", t.ItemID), "Task \""+t.Name+"\" added.")
}

func (s *Server) handleTaskEdit(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.GetTask(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.taskForm(w, r, http.StatusOK, "Edit task", *t, nil)
}

func (s *Server) handleTaskUpdate(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.GetTask(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if errs := s.taskFromForm(r, t); errs != nil {
		s.taskForm(w, r, http.StatusUnprocessableEntity, "Edit task", *t, errs)
		return
	}
	if err := s.store.SaveTask(t); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/items/%d", t.ItemID), "Task updated.")
}

func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.GetTask(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.DeleteTask(t.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/items/%d", t.ItemID), "Task deleted. Its history entries were kept.")
}

// renderComplete shows the "Mark done" form. For group items it offers the
// unit picker; ticking units fills in the supply count when the task uses
// the item's own supply (per = how many each one takes).
func (s *Server) renderComplete(w http.ResponseWriter, r *http.Request, status int, t *store.Task, c store.Completion, next string, errs []string) {
	item, err := s.store.GetItem(t.ItemID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	units, err := s.itemUnits(item)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	per := 0
	if t.SupplyID != 0 && t.SupplyID == item.SupplyID {
		per = item.SupplyPer
	}
	s.render(w, r, status, "tasks/complete", map[string]any{
		"Title": "Record: " + t.Name, "Task": t, "Next": next, "Form": c, "Units": units, "UnitSupplyPer": per, "Errors": errs,
	})
}

func (s *Server) handleTaskCompleteForm(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.GetTask(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	c := store.Completion{PerformedOn: s.today(), PerformedBy: currentUser(r).Name()}
	// Pre-tick "took it from supplies" only when the task always uses it up
	// and there's enough on hand; "sometimes" tasks start unticked.
	if t.SupplyID != 0 && t.SupplyAlways && !t.SupplyShort() {
		c.SupplyID, c.SupplyAmount = t.SupplyID, t.SupplyAmount
	}
	s.renderComplete(w, r, http.StatusOK, t, c, r.URL.Query().Get("next"), nil)
}

func (s *Server) completionFromForm(r *http.Request, c *store.Completion) []string {
	c.PerformedOn, c.PerformedBy, c.Notes = formStr(r, "performed_on"), formStr(r, "performed_by"), formStr(r, "notes")
	c.NextDueOn = formStr(r, "next_due_on")
	c.UserID = currentUser(r).ID
	var errs []string
	if !validDate(c.PerformedOn) {
		errs = append(errs, "Enter the date the work was done.")
	} else if c.PerformedOn > s.today() {
		errs = append(errs, "The work date can't be in the future.")
	}
	if c.NextDueOn != "" && !validDate(c.NextDueOn) {
		errs = append(errs, "Next due date is not valid.")
	}
	cost, ok := parseMoney(formStr(r, "cost"))
	if !ok {
		errs = append(errs, "Cost should look like 125.00")
	}
	c.CostCents = cost
	return errs
}

func (s *Server) handleTaskComplete(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.GetTask(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	item, err := s.store.GetItem(t.ItemID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	c := store.Completion{ItemID: t.ItemID, TaskID: t.ID, Units: formUnits(r, item.Quantity)}
	next := r.PostFormValue("next")
	errs := s.completionFromForm(r, &c)
	if t.SupplyID != 0 && r.PostFormValue("took_supply") == "1" {
		c.SupplyID = t.SupplyID
		amount, err := strconv.Atoi(formStr(r, "supply_amount"))
		if err != nil || amount < 1 {
			errs = append(errs, "How many "+t.Supply.Name+" were used must be 1 or more.")
		}
		c.SupplyAmount = max(amount, 1)
	}
	if errs != nil {
		s.renderComplete(w, r, http.StatusUnprocessableEntity, t, c, next, errs)
		return
	}
	var short *store.NotEnoughError
	if err := s.store.RecordMaintenance(c); errors.As(err, &short) {
		s.renderComplete(w, r, http.StatusUnprocessableEntity, t, c, next, []string{fmt.Sprintf(
			"There's %s of %s. Untick \"Took it from supplies\" if it came from somewhere else, or restock it first.", short.Error(), t.Supply.Name)})
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	if next == "" {
		next = fmt.Sprintf("/items/%d", t.ItemID)
	}
	s.redirect(w, r, safeRedirect(next), "\""+t.Name+"\" recorded for "+t.ItemName+".")
}

// Ad-hoc log entries -----------------------------------------------------

func (s *Server) renderLogForm(w http.ResponseWriter, r *http.Request, status int, item *store.Item, c store.Completion, problemID int64, errs []string) {
	units, err := s.itemUnits(item)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "logs/form", map[string]any{
		"Title": "Log work: " + item.Name, "Item": item, "Form": c, "Units": units, "ProblemID": problemID, "Errors": errs,
	})
}

func (s *Server) handleLogNew(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	c := store.Completion{PerformedOn: s.today(), PerformedBy: currentUser(r).Name()}
	if u := int(queryInt(r, "unit")); u >= 1 && u <= item.Quantity && item.Quantity > 1 {
		c.Units = []int{u}
	}
	s.renderLogForm(w, r, http.StatusOK, item, c, queryInt(r, "problem"), nil)
}

func (s *Server) handleLogCreate(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	c := store.Completion{ItemID: item.ID, Units: formUnits(r, item.Quantity)}
	problemID := formInt(r, "problem")
	errs := s.completionFromForm(r, &c)
	c.NextDueOn = ""
	if c.Notes == "" {
		errs = append(errs, "Describe the work that was done.")
	}
	if errs != nil {
		s.renderLogForm(w, r, http.StatusUnprocessableEntity, item, c, problemID, errs)
		return
	}
	if err := s.store.RecordMaintenance(c); err != nil {
		s.serverError(w, r, err)
		return
	}
	to, msg := fmt.Sprintf("/items/%d", item.ID), "Work logged."
	if problemID != 0 {
		to = fmt.Sprintf("/problems/%d", problemID)
	}
	what := item.Name
	if len(c.Units) > 0 {
		what += " " + store.UnitsLabel(c.Units)
	}
	msg += s.resolveFromFix(r, problemID, item, "Work on "+what+": "+c.Notes)
	s.redirect(w, r, to, msg)
}

func (s *Server) handleLogDelete(w http.ResponseWriter, r *http.Request) {
	itemID, err := s.store.DeleteLog(pathID(r), currentUser(r).ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/items/%d", itemID), "History entry deleted and any supplies it used were put back. Task due dates were not changed.")
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	logs, err := s.store.ListLogs(store.LogFilter{Limit: 200})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "history", map[string]any{"Title": "History", "Logs": logs})
}

// Settings & backup ------------------------------------------------------

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.Counts()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "settings", map[string]any{
		"Title": "Settings", "SiteNameValue": s.SiteName(), "DueSoonDays": s.DueSoonDays(), "PublicReportsValue": s.PublicReports(), "Counts": counts,
		"ReportURL": s.absURL(r, "/report"), "SiteURLValue": s.SiteURL(), "GuessedURL": s.guessedURL(r),
	})
}

func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	name := formStr(r, "site_name")
	days, err := strconv.Atoi(formStr(r, "due_soon_days"))
	siteURL, urlErr := normalizeSiteURL(formStr(r, "site_url"))
	var errs []string
	if urlErr != nil {
		errs = append(errs, urlErr.Error())
	}
	if name == "" {
		errs = append(errs, "Site name is required.")
	}
	if err != nil || days < 1 || days > 365 {
		errs = append(errs, "\"Due soon\" window must be between 1 and 365 days.")
	}
	if errs != nil {
		counts, _ := s.store.Counts()
		s.render(w, r, http.StatusUnprocessableEntity, "settings", map[string]any{
			"Title": "Settings", "SiteNameValue": name, "DueSoonDays": formStr(r, "due_soon_days"), "Counts": counts, "Errors": errs,
			"PublicReportsValue": r.PostFormValue("public_reports") == "1", "ReportURL": s.absURL(r, "/report"),
			"SiteURLValue": formStr(r, "site_url"), "GuessedURL": s.guessedURL(r),
		})
		return
	}
	if err := s.store.SetSetting("site_name", name); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.SetSetting("due_soon_days", strconv.Itoa(days)); err != nil {
		s.serverError(w, r, err)
		return
	}
	public := "0"
	if r.PostFormValue("public_reports") == "1" {
		public = "1"
	}
	if err := s.store.SetSetting("public_reports", public); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.SetSetting("site_url", siteURL); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.reloadSettings()
	s.redirect(w, r, "/admin/settings", "Settings saved.")
}

// handleBackup streams a consistent snapshot of the database. Restoring is
// just replacing the .db file on the target host while the app is stopped.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	dir, err := os.MkdirTemp("", "mt-backup-")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "backup.db")
	if err := s.store.Backup(r.Context(), dest); err != nil {
		s.serverError(w, r, err)
		return
	}
	f, err := os.Open(dest)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	defer f.Close()
	name := "maintenance-" + time.Now().Format("2006-01-02-1504") + ".db"
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	io.Copy(w, f)
}

// normalizeSiteURL checks the site address from Settings and trims it to
// "scheme://host[:port][/path]" with no trailing slash. Blank is allowed.
func normalizeSiteURL(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("Site address should look like https://maintenance.example.org")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}

// guessedURL is the address worked out from this request, shown in
// Settings as a hint.
func (s *Server) guessedURL(r *http.Request) string {
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
