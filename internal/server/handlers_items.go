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
	f := store.ItemFilter{PlaceID: queryInt(r, "place"), Tag: r.URL.Query().Get("tag"), Query: r.URL.Query().Get("q")}
	items, err := s.store.ListItems(f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "items/index", map[string]any{
		"Title": "Items", "Items": items, "Places": tree.All(), "Tags": tree.Tags(), "Filter": f,
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
	// UnitIDs lists each unit's ID in order, comma-separated; blanks are
	// given one.
	UnitIDs string
}

func (s *Server) renderItemForm(w http.ResponseWriter, r *http.Request, status int, title string, f itemForm, errs []string) {
	tree, err := s.store.Places()
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
	products, err := s.store.ListProducts(store.ProductFilter{})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Suggest the products there are, then common things not yet added.
	names := slices.Clone(commonItems)
	names = slices.DeleteFunc(names, func(n string) bool {
		return slices.ContainsFunc(products, func(p store.Product) bool { return strings.EqualFold(p.Name, n) })
	})
	if f.SupplySource == "" {
		f.SupplySource = "none"
		if f.SupplyID != 0 {
			f.SupplySource = "existing"
		}
	}
	s.render(w, r, status, "items/form", map[string]any{
		"Title": title, "Form": f, "Places": tree.All(), "Supplies": supplies,
		"Categories": cats, "Products": products, "Names": names, "Errors": errs,
	})
}

func (s *Server) itemFromForm(r *http.Request, f *itemForm) []string {
	f.PlaceID = formInt(r, "place_id")
	f.Name, f.Category = formStr(r, "name"), formStr(r, "category")
	f.Manufacturer, f.Model, f.SerialNumber = formStr(r, "manufacturer"), formStr(r, "model"), formStr(r, "serial_number")
	f.InstallDate, f.Notes = formStr(r, "install_date"), formStr(r, "notes")
	f.Portable = r.PostFormValue("portable") == "1"
	f.Counted = r.PostFormValue("tracking") == "count"
	f.SupplySource = formStr(r, "supply_source")
	var errs []string
	if _, err := s.store.GetPlace(f.PlaceID); err != nil {
		errs = append(errs, "Choose where it is.")
	}
	if f.Name == "" {
		errs = append(errs, "Name is required.")
	} else if msg := tooLong("Name", f.Name, 100); msg != nil {
		errs = append(errs, msg...)
	}
	// A product it's already one of decides its category and counting.
	f.ProductID = 0
	if p, err := s.store.ProductByName(f.Name); err == nil {
		f.ProductID, f.Name, f.Category, f.Counted = p.ID, p.Name, p.Category, p.Counted
	}
	var err error
	f.Quantity = 1
	if v := formStr(r, "quantity"); v != "" {
		if f.Quantity, err = strconv.Atoi(v); err != nil || f.Quantity < 1 {
			errs = append(errs, "How many must be 1 or more.")
			f.Quantity = max(f.Quantity, 1)
		}
	}
	f.UnitIDs = formStr(r, "unit_ids")
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
	f := itemForm{Item: store.Item{PlaceID: queryInt(r, "place"), Quantity: 1, SupplyPer: 1}}
	if p, err := s.store.GetProduct(queryInt(r, "product")); err == nil {
		f.ProductID, f.Name, f.Category, f.Counted = p.ID, p.Name, p.Category, p.Counted
	}
	s.renderItemForm(w, r, http.StatusOK, "Add item", f, nil)
}

func (s *Server) handleItemCreate(w http.ResponseWriter, r *http.Request) {
	var f itemForm
	errs := s.itemFromForm(r, &f)
	idErrs, err := s.checkItemUnitIDs(&f, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if errs = append(errs, idErrs...); errs != nil {
		s.renderItemForm(w, r, http.StatusUnprocessableEntity, "Add item", f, errs)
		return
	}
	if err := s.saveItem(r, &f); err != nil {
		s.serverError(w, r, err)
		return
	}
	if ids := parseUnitIDs(f.UnitIDs); ids != nil && !f.Counted {
		if err := s.store.SetUnitIDs(f.ID, f.Quantity, ids); err != nil {
			s.serverError(w, r, err)
			return
		}
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
	units, err := s.store.ItemUnits(item.ID, item.Quantity, s.todayTime())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// A single item has no units table, so its ID goes with its details
	// when it's one that moves or has been given its own. Counted items
	// have neither.
	var onlyID string
	if item.Counted {
		units = nil
	} else if item.Quantity == 1 {
		if item.Portable || units[0].Tagged() {
			onlyID = units[0].ID()
		}
		units = nil
	}
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	crumbs := append(tree.Ancestors(item.PlaceID), item.Place)
	product, err := s.store.GetProduct(item.ProductID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "items/show", map[string]any{
		"Title": item.Name, "Item": item, "Product": product, "Crumbs": crumbs, "Tasks": active, "DoneTasks": done, "Logs": logs, "SuppliesUsed": supplies, "Problems": problems,
		"Units": units, "OnlyID": onlyID, "FrequentReplacements": store.FrequentReplacements,
	})
}

func (s *Server) handleItemEdit(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ids, err := s.unitIDsField(item)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderItemForm(w, r, http.StatusOK, "Edit "+item.Name, itemForm{Item: *item, UnitIDs: ids}, nil)
}

func (s *Server) handleItemUpdate(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	was, err := s.unitIDsField(item)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := itemForm{Item: *item}
	errs := s.itemFromForm(r, &f)
	idErrs, err := s.checkItemUnitIDs(&f, was)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if errs = append(errs, idErrs...); errs != nil {
		s.renderItemForm(w, r, http.StatusUnprocessableEntity, "Edit item", f, errs)
		return
	}
	// A new location is recorded in the item's history as a move.
	if _, err := s.store.MoveItem(store.Move{ItemID: item.ID, PlaceID: f.PlaceID, MovedOn: s.today(), UserID: currentUser(r).ID}); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.saveItem(r, &f); err != nil {
		s.serverError(w, r, err)
		return
	}
	// IDs are only rewritten when the list was changed; otherwise new
	// units are given theirs as the item is saved.
	if ids := parseUnitIDs(f.UnitIDs); !f.Counted && !slices.Equal(ids, parseUnitIDs(was)) {
		if err := s.store.SetUnitIDs(item.ID, f.Quantity, ids); err != nil {
			s.serverError(w, r, err)
			return
		}
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
	s.redirect(w, r, fmt.Sprintf("/places/%d", item.PlaceID), item.Name+" deleted.")
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
	if u := int(queryInt(r, "unit")); u >= 1 && u <= item.Quantity && item.HasUnits() {
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
		Notes: formStr(r, "notes"), UserID: user.ID, Units: formUnits(r, item)}
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
			if sp.Stock() != "ok" {
				msg += " Time to reorder."
			}
		}
	}
	msg += s.resolveFromFix(r, problemID, item, fmt.Sprintf("Replaced %d × %s in %s.", c.Replaced, item.Supply.Name, what))
	s.redirect(w, r, safeRedirect(next), msg)
}

// Moving -------------------------------------------------------------------

func (s *Server) renderMoveForm(w http.ResponseWriter, r *http.Request, status int, item *store.Item, m store.Move, next string, errs []string) {
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	units, err := s.itemUnits(item)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "items/move", map[string]any{
		"Title": "Move " + item.Name, "Item": item, "Form": m, "Places": tree.All(), "Units": units, "Next": next, "Errors": errs,
	})
}

func (s *Server) handleMoveForm(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderMoveForm(w, r, http.StatusOK, item, store.Move{PlaceID: item.PlaceID, Count: item.Quantity, MovedOn: s.today()}, r.URL.Query().Get("next"), nil)
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	next := r.PostFormValue("next")
	m := store.Move{ItemID: item.ID, PlaceID: formInt(r, "place_id"), MovedOn: formStr(r, "moved_on"), Note: formStr(r, "note"), UserID: currentUser(r).ID,
		Units: formUnits(r, item), Count: item.Quantity}
	var errs []string
	if _, err := s.store.GetPlace(m.PlaceID); err != nil {
		errs = append(errs, "Choose where it's moving to.")
	}
	if !validDate(m.MovedOn) {
		errs = append(errs, "Enter the date it was moved.")
	} else if m.MovedOn > s.today() {
		errs = append(errs, "The date can't be in the future.")
	}
	if m.PlaceID == item.PlaceID {
		errs = append(errs, "Choose where it's moving to; that's where it is now.")
	}
	// Ticked units say how many on their own; otherwise it's the count.
	if len(m.Units) > 0 {
		m.Count = len(m.Units)
	} else if item.Quantity > 1 {
		if m.Count, err = strconv.Atoi(formStr(r, "count")); err != nil || m.Count < 1 || m.Count > item.Quantity {
			errs = append(errs, fmt.Sprintf("How many must be between 1 and %d.", item.Quantity))
			m.Count = item.Quantity
		}
	}
	if errs != nil {
		s.renderMoveForm(w, r, http.StatusUnprocessableEntity, item, m, next, errs)
		return
	}
	into, err := s.store.MoveItem(m)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if next == "" {
		next = fmt.Sprintf("/items/%d", item.ID)
	}
	moved, err := s.store.GetItem(into)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if into == item.ID {
		s.redirect(w, r, safeRedirect(next), item.Name+" moved to "+moved.Location()+".")
		return
	}
	s.redirect(w, r, safeRedirect(next), fmt.Sprintf("Moved %d of %d %s to %s; %d left here.",
		m.Count, item.Quantity, item.Name, moved.Location(), item.Quantity-m.Count))
}

// Units -------------------------------------------------------------------

// formUnits reads the "Which ones" checkboxes, dropping anything that
// isn't one of the item's units.
func formUnits(r *http.Request, item *store.Item) []int {
	if !item.HasUnits() {
		return nil
	}
	var units []int
	for _, v := range r.PostForm["units"] {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= item.Quantity && !slices.Contains(units, n) {
			units = append(units, n)
		}
	}
	slices.Sort(units)
	return units
}

// itemUnits returns a group item's units for pickers; nil for single and
// counted items.
func (s *Server) itemUnits(item *store.Item) ([]store.Unit, error) {
	if !item.HasUnits() {
		return nil, nil
	}
	return s.store.ItemUnits(item.ID, item.Quantity, s.todayTime())
}

// parseUnitIDs splits "Mic 1, Mic 2" into IDs, dropping blanks at the
// end; nil when there are none.
func parseUnitIDs(v string) []string {
	ids := strings.Split(v, ",")
	for i := range ids {
		ids[i] = strings.TrimSpace(ids[i])
	}
	for len(ids) > 0 && ids[len(ids)-1] == "" {
		ids = ids[:len(ids)-1]
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// checkUnitIDs reports problems with IDs for units #1..#quantity, in
// order (blank to be given one). taken are those other items of the same
// kind use; kind names them.
func checkUnitIDs(ids []string, quantity int, kind string, taken map[string]bool) []string {
	var errs []string
	if len(ids) > quantity {
		errs = append(errs, fmt.Sprintf("There are %d IDs listed for %d of them.", len(ids), quantity))
	}
	seen := map[string]bool{}
	var dups, clash []string
	for _, id := range ids {
		if id == "" {
			continue
		}
		if msg := tooLong("Each ID", id, 40); msg != nil && !slices.Contains(errs, msg[0]) {
			errs = append(errs, msg...)
		}
		low := strings.ToLower(id)
		if seen[low] && !slices.Contains(dups, id) {
			dups = append(dups, id)
		}
		if taken[low] && !slices.Contains(clash, id) {
			clash = append(clash, id)
		}
		seen[low] = true
	}
	if dups != nil {
		errs = append(errs, "Each one needs its own ID; more than one is "+strings.Join(dups, ", ")+".")
	}
	if clash != nil {
		errs = append(errs, fmt.Sprintf("Other %s already use %s.", kind, strings.Join(clash, ", ")))
	}
	return errs
}

// checkItemUnitIDs checks the IDs on the item form against the others of
// its kind. was is the list as the form showed it; left alone, it only
// needs to cover however many there are now.
func (s *Server) checkItemUnitIDs(f *itemForm, was string) ([]string, error) {
	if f.Counted {
		return nil, nil
	}
	ids := parseUnitIDs(f.UnitIDs)
	if slices.Equal(ids, parseUnitIDs(was)) && len(ids) > f.Quantity {
		ids = ids[:f.Quantity]
	}
	taken, err := s.store.UnitIDsTaken(f.ID, f.Name, f.Portable)
	if err != nil {
		return nil, err
	}
	return checkUnitIDs(ids, f.Quantity, f.Name, taken), nil
}

// unitIDsField renders an item's unit IDs for the item form.
func (s *Server) unitIDsField(item *store.Item) (string, error) {
	if item.Counted {
		return "", nil
	}
	units, err := s.store.ItemUnits(item.ID, item.Quantity, s.todayTime())
	if err != nil {
		return "", err
	}
	ids := make([]string, len(units))
	for i, u := range units {
		ids[i] = u.ID()
	}
	return strings.Join(ids, ", "), nil
}

func (s *Server) handleUnitsForm(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetItem(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if item.Counted {
		s.setFlash(w, r, "info", item.Name+" are only counted, so they don't have IDs or notes of their own.")
		http.Redirect(w, r, fmt.Sprintf("/items/%d", item.ID), http.StatusSeeOther)
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
	if item.Counted {
		s.setFlash(w, r, "info", item.Name+" are only counted, so they don't have IDs or notes of their own.")
		http.Redirect(w, r, fmt.Sprintf("/items/%d", item.ID), http.StatusSeeOther)
		return
	}
	units := make([]store.Unit, item.Quantity)
	ids := make([]string, item.Quantity)
	var errs []string
	for i := range units {
		n := strconv.Itoa(i + 1)
		units[i] = store.Unit{Number: i + 1, Tag: formStr(r, "id_"+n), Label: formStr(r, "label_"+n)}
		ids[i] = units[i].Tag
		if strings.Contains(units[i].Tag, ",") {
			errs = append(errs, fmt.Sprintf("#%s: IDs can't contain commas.", n))
		}
		if msg := tooLong("Location note", units[i].Label, 100); msg != nil {
			errs = append(errs, fmt.Sprintf("#%s: %s", n, msg[0]))
		}
	}
	taken, err := s.store.UnitIDsTaken(item.ID, item.Name, item.Portable)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if errs = append(errs, checkUnitIDs(ids, item.Quantity, item.Name, taken)...); errs != nil {
		s.setFlash(w, r, "error", strings.Join(errs, " "))
		http.Redirect(w, r, fmt.Sprintf("/items/%d/units", item.ID), http.StatusSeeOther)
		return
	}
	if err := s.store.SaveUnits(item.ID, units); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/items/%d", item.ID), "Unit IDs and notes saved.")
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
