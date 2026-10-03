package server

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// Items ------------------------------------------------------------------

// commonItems are suggested in the name field.
var commonItems = []string{
	"Lights", "Electrical outlets", "Light switches", "Air returns", "Air vents", "Smoke detectors", "Exit signs",
	"Ceiling fans", "Mini-split AC", "Furnace", "Water heater", "Fire extinguisher", "Projector", "TV", "Windows", "Doors",
}

// itemGroup is the items of one category, for room and building pages.
type itemGroup struct {
	Category string
	Items    []store.Item
}

// groupItems buckets items by category, alphabetically with uncategorised
// items last, keeping each category's items in name order.
func groupItems(items []store.Item) []itemGroup {
	var groups []itemGroup
	for _, it := range items {
		i := slices.IndexFunc(groups, func(g itemGroup) bool { return strings.EqualFold(g.Category, it.Category) })
		if i < 0 {
			groups = append(groups, itemGroup{Category: it.Category})
			i = len(groups) - 1
		}
		groups[i].Items = append(groups[i].Items, it)
	}
	slices.SortFunc(groups, func(a, b itemGroup) int {
		if (a.Category == "") != (b.Category == "") {
			if a.Category == "" {
				return 1
			}
			return -1
		}
		return cmp.Compare(strings.ToLower(a.Category), strings.ToLower(b.Category))
	})
	for _, g := range groups {
		slices.SortStableFunc(g.Items, func(a, b store.Item) int {
			return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
	}
	return groups
}

func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	f := store.ItemFilter{BuildingID: queryInt(r, "building"), Query: r.URL.Query().Get("q")}
	items, err := s.store.ListItems(f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	buildings, err := s.store.ListBuildings()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "items/index", map[string]any{
		"Title": "Items", "Items": items, "Buildings": buildings, "Filter": f,
	})
}

// itemForm holds the item plus the choice of supply: "none", an
// "existing" one, or a "new" one to create.
type itemForm struct {
	store.Item
	SupplySource  string
	NewSupplyName string
	NewSupplyUnit string
	NewSupplyQty  int
}

func (s *Server) renderItemForm(w http.ResponseWriter, r *http.Request, status int, title string, f itemForm, errs []string) {
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
	// Replacing uses the supply up, so only consumables can be linked.
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
	cats, _ := s.store.Categories()
	if f.SupplySource == "" {
		f.SupplySource = "none"
		if f.SupplyID != 0 {
			f.SupplySource = "existing"
		}
	}
	s.render(w, r, status, "items/form", map[string]any{
		"Title": title, "Form": f, "Buildings": buildings, "Rooms": rooms, "Supplies": supplies,
		"Categories": cats, "Names": commonItems, "Errors": errs,
	})
}

func (s *Server) itemFromForm(r *http.Request, f *itemForm) []string {
	f.BuildingID, f.RoomID = formInt(r, "building_id"), formInt(r, "room_id")
	f.Name, f.Category = formStr(r, "name"), formStr(r, "category")
	f.Manufacturer, f.Model, f.SerialNumber = formStr(r, "manufacturer"), formStr(r, "model"), formStr(r, "serial_number")
	f.InstallDate, f.Notes = formStr(r, "install_date"), formStr(r, "notes")
	f.Portable = r.PostFormValue("portable") == "1"
	f.SupplySource = formStr(r, "supply_source")
	var errs []string
	if f.BuildingID == 0 {
		errs = append(errs, "Choose a building.")
	}
	if f.Name == "" {
		errs = append(errs, "Name is required.")
	}
	var err error
	f.Quantity = 1
	if v := formStr(r, "quantity"); v != "" {
		if f.Quantity, err = strconv.Atoi(v); err != nil || f.Quantity < 1 {
			errs = append(errs, "How many must be 1 or more.")
			f.Quantity = max(f.Quantity, 1)
		}
	}
	if f.InstallDate != "" && !validDate(f.InstallDate) {
		errs = append(errs, "Install date is not a valid date.")
	}

	f.SupplyID, f.SupplyPer = 0, 1
	if f.SupplySource == "existing" || f.SupplySource == "new" {
		if f.SupplyPer, err = strconv.Atoi(formStr(r, "supply_per")); err != nil || f.SupplyPer < 1 {
			errs = append(errs, "How many of the supply each one uses must be 1 or more.")
			f.SupplyPer = 1
		}
	}
	switch f.SupplySource {
	case "existing":
		f.SupplyID = formInt(r, "supply_id")
		if sp, err := s.store.GetSupply(f.SupplyID); err != nil {
			errs = append(errs, "Choose the supply it uses.")
		} else if sp.Reusable {
			errs = append(errs, sp.Name+" is reusable; items can only use supplies that get used up.")
		}
	case "new":
		f.NewSupplyName, f.NewSupplyUnit = formStr(r, "new_supply_name"), formStr(r, "new_supply_unit")
		if f.NewSupplyName == "" {
			errs = append(errs, "Enter a name for the new supply.")
		}
		var ok bool
		if f.NewSupplyQty, ok = formCount(r, "new_supply_quantity"); !ok {
			errs = append(errs, "On hand must be a whole number, 0 or more.")
		}
	case "none", "":
	default:
		errs = append(errs, "Choose whether it uses a supply.")
	}

	if f.RoomID != 0 {
		room, err := s.store.GetRoom(f.RoomID)
		if err != nil || room.BuildingID != f.BuildingID {
			errs = append(errs, "The selected room is not in the selected building.")
		}
	}
	return errs
}

// saveItem saves the form, creating the new supply when asked.
func (s *Server) saveItem(r *http.Request, f *itemForm) error {
	var sp *store.Supply
	if f.SupplySource == "new" {
		sp = &store.Supply{Name: f.NewSupplyName, Unit: f.NewSupplyUnit, Quantity: f.NewSupplyQty}
	}
	return s.store.SaveItem(&f.Item, sp, currentUser(r).ID)
}

func (s *Server) handleItemNew(w http.ResponseWriter, r *http.Request) {
	f := itemForm{Item: store.Item{BuildingID: queryInt(r, "building"), RoomID: queryInt(r, "room"), Quantity: 1, SupplyPer: 1}}
	if f.RoomID != 0 {
		if room, err := s.store.GetRoom(f.RoomID); err == nil {
			f.BuildingID = room.BuildingID
		}
	}
	s.renderItemForm(w, r, http.StatusOK, "Add item", f, nil)
}

func (s *Server) handleItemCreate(w http.ResponseWriter, r *http.Request) {
	var f itemForm
	if errs := s.itemFromForm(r, &f); errs != nil {
		s.renderItemForm(w, r, http.StatusUnprocessableEntity, "Add item", f, errs)
		return
	}
	if err := s.saveItem(r, &f); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/items/%d", f.ID), f.Name+" added.")
}

func (s *Server) handleItemShow(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tasks, err := s.store.ListTasks(store.TaskFilter{ItemID: item.ID})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	logs, err := s.store.ListLogs(store.LogFilter{ItemID: item.ID})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	problems, err := s.store.ListProblems(store.ProblemFilter{ItemID: item.ID, Status: "active"})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var active, done []store.Task
	var supplies []store.Task // one active task per other supply used, for the summary
	seen := map[int64]bool{item.SupplyID: true}
	for _, t := range tasks {
		if t.Active {
			active = append(active, t)
			if t.SupplyID != 0 && !seen[t.SupplyID] {
				seen[t.SupplyID] = true
				supplies = append(supplies, t)
			}
		} else {
			done = append(done, t)
		}
	}
	units, err := s.itemUnits(item)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "items/show", map[string]any{
		"Title": item.Name, "Item": item, "Tasks": active, "DoneTasks": done, "Logs": logs, "SuppliesUsed": supplies, "Problems": problems,
		"Units": units, "FrequentReplacements": store.FrequentReplacements,
	})
}

func (s *Server) handleItemEdit(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderItemForm(w, r, http.StatusOK, "Edit "+item.Name, itemForm{Item: *item}, nil)
}

func (s *Server) handleItemUpdate(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := itemForm{Item: *item}
	if errs := s.itemFromForm(r, &f); errs != nil {
		s.renderItemForm(w, r, http.StatusUnprocessableEntity, "Edit item", f, errs)
		return
	}
	// A new location is recorded in the item's history as a move.
	if err := s.store.MoveItem(store.Move{ItemID: item.ID, BuildingID: f.BuildingID, RoomID: f.RoomID, MovedOn: s.today(), UserID: currentUser(r).ID}); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.saveItem(r, &f); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/items/%d", item.ID), "Item updated.")
}

func (s *Server) handleItemDelete(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.DeleteItem(item.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	to := fmt.Sprintf("/buildings/%d", item.BuildingID)
	if item.RoomID != 0 {
		to = fmt.Sprintf("/rooms/%d", item.RoomID)
	}
	s.redirect(w, r, to, item.Name+" deleted.")
}

// Replacing an item's supply -------------------------------------------------

func (s *Server) renderReplaceForm(w http.ResponseWriter, r *http.Request, status int, item *store.Item, c store.Completion, next string, problemID int64, errs []string) {
	units, err := s.itemUnits(item)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "items/replace", map[string]any{
		"Title": "Replace " + item.Supply.Name, "Item": item, "Form": c, "Units": units, "Next": next, "ProblemID": problemID, "Errors": errs,
	})
}

func (s *Server) handleReplaceForm(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if item.SupplyID == 0 {
		s.setFlash(w, r, "info", "Set the supply "+item.Name+" uses before recording a replacement.")
		http.Redirect(w, r, fmt.Sprintf("/items/%d/edit", item.ID), http.StatusSeeOther)
		return
	}
	// Default to one's worth, taking it from stock when that much is on hand.
	c := store.Completion{PerformedOn: s.today(), Replaced: item.SupplyPer}
	if !item.SupplyShort() {
		c.SupplyID = item.SupplyID
	}
	if u := int(queryInt(r, "unit")); u >= 1 && u <= item.Quantity && item.Quantity > 1 {
		c.Units = []int{u}
	}
	s.renderReplaceForm(w, r, http.StatusOK, item, c, r.URL.Query().Get("next"), queryInt(r, "problem"), nil)
}

func (s *Server) handleReplace(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if item.SupplyID == 0 {
		http.Redirect(w, r, fmt.Sprintf("/items/%d/edit", item.ID), http.StatusSeeOther)
		return
	}
	next, problemID := r.PostFormValue("next"), formInt(r, "problem")
	user := currentUser(r)
	c := store.Completion{ItemID: item.ID, Kind: "replaced", PerformedOn: formStr(r, "performed_on"), PerformedBy: user.Name(),
		Notes: formStr(r, "notes"), UserID: user.ID, Units: formUnits(r, item.Quantity)}
	var errs []string
	if !validDate(c.PerformedOn) {
		errs = append(errs, "Enter the date it was replaced.")
	} else if c.PerformedOn > s.today() {
		errs = append(errs, "The date can't be in the future.")
	}
	if c.Replaced, err = strconv.Atoi(formStr(r, "replaced")); err != nil || c.Replaced < 1 {
		errs = append(errs, "How many must be 1 or more.")
		c.Replaced = max(c.Replaced, 1)
	}
	if r.PostFormValue("took_supply") == "1" {
		c.SupplyID, c.SupplyAmount = item.SupplyID, c.Replaced
	}
	if errs != nil {
		s.renderReplaceForm(w, r, http.StatusUnprocessableEntity, item, c, next, problemID, errs)
		return
	}
	var short *store.NotEnoughError
	if err := s.store.RecordMaintenance(c); errors.As(err, &short) {
		s.renderReplaceForm(w, r, http.StatusUnprocessableEntity, item, c, next, problemID, []string{fmt.Sprintf(
			"There's %s of %s. Untick \"Took it from supplies\" if it came from somewhere else, or restock first.", short.Error(), item.Supply.Name)})
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	if next == "" {
		next = fmt.Sprintf("/items/%d", item.ID)
	}
	what := item.Name
	if len(c.Units) > 0 {
		what += " " + store.UnitsLabel(c.Units)
	}
	msg := fmt.Sprintf("Recorded %d × %s replaced in %s.", c.Replaced, item.Supply.Name, what)
	if c.SupplyID != 0 {
		if sp, err := s.store.GetSupply(item.SupplyID); err == nil {
			msg += fmt.Sprintf(" %s on hand.", sp.QuantityLabel())
			if sp.NeedsRestock() {
				msg += " Time to reorder."
			}
		}
	}
	msg += s.resolveFromFix(r, problemID, item, fmt.Sprintf("Replaced %d × %s in %s.", c.Replaced, item.Supply.Name, what))
	s.redirect(w, r, safeRedirect(next), msg)
}

// Moving -------------------------------------------------------------------

func (s *Server) renderMoveForm(w http.ResponseWriter, r *http.Request, status int, item *store.Item, m store.Move, next string, errs []string) {
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
	s.render(w, r, status, "items/move", map[string]any{
		"Title": "Move " + item.Name, "Item": item, "Form": m, "Buildings": buildings, "Rooms": rooms, "Next": next, "Errors": errs,
	})
}

func (s *Server) handleMoveForm(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderMoveForm(w, r, http.StatusOK, item, store.Move{BuildingID: item.BuildingID, RoomID: item.RoomID, MovedOn: s.today()}, r.URL.Query().Get("next"), nil)
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	next := r.PostFormValue("next")
	m := store.Move{ItemID: item.ID, BuildingID: formInt(r, "building_id"), RoomID: formInt(r, "room_id"),
		MovedOn: formStr(r, "moved_on"), Note: formStr(r, "note"), UserID: currentUser(r).ID}
	var errs []string
	if _, err := s.store.GetBuilding(m.BuildingID); err != nil {
		errs = append(errs, "Choose a building.")
	}
	if m.RoomID != 0 {
		room, err := s.store.GetRoom(m.RoomID)
		if err != nil || room.BuildingID != m.BuildingID {
			errs = append(errs, "The selected room is not in the selected building.")
		}
	}
	if !validDate(m.MovedOn) {
		errs = append(errs, "Enter the date it was moved.")
	} else if m.MovedOn > s.today() {
		errs = append(errs, "The date can't be in the future.")
	}
	if m.BuildingID == item.BuildingID && m.RoomID == item.RoomID {
		errs = append(errs, "Choose where it's moving to; that's where it is now.")
	}
	if errs != nil {
		s.renderMoveForm(w, r, http.StatusUnprocessableEntity, item, m, next, errs)
		return
	}
	if err := s.store.MoveItem(m); err != nil {
		s.serverError(w, r, err)
		return
	}
	if next == "" {
		next = fmt.Sprintf("/items/%d", item.ID)
	}
	moved, err := s.store.GetItem(item.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, safeRedirect(next), item.Name+" moved to "+moved.Location()+".")
}

// Units -------------------------------------------------------------------

// formUnits reads the "Which ones" checkboxes, dropping anything that
// isn't a unit of an item with this many.
func formUnits(r *http.Request, quantity int) []int {
	var units []int
	for _, v := range r.PostForm["units"] {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= quantity && !slices.Contains(units, n) {
			units = append(units, n)
		}
	}
	slices.Sort(units)
	return units
}

// itemUnits returns a group item's units for pickers; nil for single items.
func (s *Server) itemUnits(item *store.Item) ([]store.Unit, error) {
	if item.Quantity < 2 {
		return nil, nil
	}
	return s.store.ItemUnits(item.ID, item.Quantity, s.todayTime())
}

func (s *Server) handleUnitsForm(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	units, err := s.store.ItemUnits(item.ID, item.Quantity, s.todayTime())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "items/units", map[string]any{"Title": "Number " + item.Name, "Item": item, "Units": units})
}

func (s *Server) handleUnitsSave(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	labels := map[int]string{}
	for n := 1; n <= item.Quantity; n++ {
		label := formStr(r, "label_"+strconv.Itoa(n))
		if msg := tooLong("Location note", label, 100); msg != nil {
			s.setFlash(w, r, "error", fmt.Sprintf("#%d: %s", n, msg[0]))
			http.Redirect(w, r, fmt.Sprintf("/items/%d/units", item.ID), http.StatusSeeOther)
			return
		}
		labels[n] = label
	}
	if err := s.store.SaveUnitLabels(item.ID, labels); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/items/%d", item.ID), "Unit notes saved.")
}

// resolveFromFix marks a problem resolved after work on its item was
// recorded from the problem page. It returns text for the flash message,
// or "" when there was nothing to resolve.
func (s *Server) resolveFromFix(r *http.Request, problemID int64, item *store.Item, note string) string {
	if problemID == 0 {
		return ""
	}
	p, err := s.store.GetProblem(problemID)
	if err != nil || p.ItemID != item.ID || !p.Active() {
		return ""
	}
	user := currentUser(r)
	assignee := p.AssignedTo
	if assignee == 0 {
		assignee = user.ID
	}
	if _, err := s.store.UpdateProblem(store.ProblemChange{ProblemID: p.ID, Status: store.ProblemResolved, AssignedTo: assignee, Note: note, UserID: user.ID}); err != nil {
		s.log.Error("resolve problem", "problem", p.ID, "err", err)
		return ""
	}
	return fmt.Sprintf(" Problem \"%s\" marked resolved.", p.Title)
}
