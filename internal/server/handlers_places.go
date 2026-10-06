package server

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// Places ---------------------------------------------------------------------

// handlePlaces shows the whole tree, or with ?tag= the places with a tag.
func (s *Server) handlePlaces(w http.ResponseWriter, r *http.Request) {
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if tag := r.URL.Query().Get("tag"); tag != "" {
		s.renderTag(w, r, tree, tag)
		return
	}
	tasks, err := s.store.ListTasks(store.TaskFilter{ActiveOnly: true})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	health := map[int64]buildingHealth{}
	for _, h := range s.buildingHealth(tree, tasks) {
		health[h.ID] = h
	}
	_, single := tree.SingleSite()
	s.render(w, r, http.StatusOK, "places/index", map[string]any{
		"Title": "Places", "Places": tree.All(), "Tags": tree.Tags(), "Health": health, "SingleSite": single,
	})
}

// renderTag lists the places with a tag and what's reported in them.
func (s *Server) renderTag(w http.ResponseWriter, r *http.Request, tree *store.Places, tag string) {
	places := tree.Tagged(tag)
	if len(places) == 0 {
		s.notFound(w, r)
		return
	}
	problems, err := s.store.ListProblems(store.ProblemFilter{Tag: tag, Status: "active"})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	items, total := 0, 0
	for _, p := range places {
		items += p.ItemTotal
		total += p.ProblemTotal
	}
	s.render(w, r, http.StatusOK, "places/tag", map[string]any{
		"Title": "Tagged “" + tag + "”", "Tag": places[0].Tags[slices.IndexFunc(places[0].Tags, func(t string) bool { return strings.EqualFold(t, tag) })],
		"Places": places, "Problems": problems, "ItemTotal": items,
	})
}

// placeForm holds the form plus whatever it needs to pick from.
type placeForm struct {
	store.Place
	TagText string
}

func (s *Server) renderPlaceForm(w http.ResponseWriter, r *http.Request, status int, title string, f placeForm, errs []string) {
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// A place can't go inside itself or anything inside it.
	var parents []store.Place
	for _, p := range tree.All() {
		if f.ID == 0 || !tree.Within(p.ID, f.ID) {
			parents = append(parents, p)
		}
	}
	tags := tree.Tags()
	s.render(w, r, status, "places/form", map[string]any{
		"Title": title, "Form": f, "Parents": parents, "Kinds": store.PlaceKinds, "Tags": tags,
		"Empty": tree.Len() == 0, "Errors": errs,
	})
}

// defaultKind is what most often goes inside a place of this kind.
func defaultKind(parent string) string {
	switch parent {
	case "", store.KindSite:
		return store.KindBuilding
	case store.KindRoom:
		return store.KindArea
	}
	return store.KindRoom
}

// placeFromForm reads the form and checks the place fits in the tree.
func (s *Server) placeFromForm(r *http.Request, f *placeForm) []string {
	f.Kind, f.ParentID = formStr(r, "kind"), formInt(r, "parent_id")
	f.Number, f.Name, f.Notes = formStr(r, "number"), formStr(r, "name"), formStr(r, "notes")
	f.Address, f.TagText = "", formStr(r, "tags")
	if f.Kind == store.KindSite || f.Kind == store.KindBuilding {
		f.Address = formStr(r, "address")
	}
	if f.Kind == store.KindSite {
		f.Number = ""
	}
	f.Tags = store.ParseTags(f.TagText)
	f.AlsoIn = nil
	if f.Kind == store.KindArea {
		for _, v := range r.PostForm["also_in"] {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil && id != f.ParentID {
				f.AlsoIn = append(f.AlsoIn, id)
			}
		}
	}
	var errs []string
	if f.Name == "" {
		errs = append(errs, "Name is required.")
	}
	errs = append(errs, tooLong("Name", f.Name, 100)...)
	errs = append(errs, tooLong("Number", f.Number, 20)...)
	for _, tag := range f.Tags {
		errs = append(errs, tooLong("Each tag", tag, 40)...)
	}
	if len(f.Tags) > 10 {
		errs = append(errs, "Use 10 tags or fewer.")
	}
	return append(errs, s.checkPlacement(&f.Place)...)
}

// checkPlacement checks a place's kind and parents against the tree rules
// (see store.KindFits): it must fit where it goes, what's already inside
// it must still fit in it, and nothing can end up inside itself. A
// building with no parent goes on the only site (created if there are none
// yet).
func (s *Server) checkPlacement(p *store.Place) []string {
	if !store.ValidKind(p.Kind) {
		return []string{"Choose what kind of place it is."}
	}
	if p.Kind == store.KindSite {
		p.ParentID = 0
	} else if p.ParentID == 0 {
		site, err := s.store.EnsureSite(s.SiteName())
		if err != nil || site == 0 {
			return []string{"Choose where it is."}
		}
		p.ParentID = site
	}
	tree, err := s.store.Places()
	if err != nil {
		return []string{"Couldn't check where it goes. Please try again."}
	}
	var errs []string
	if p.ID != 0 && p.ParentID != 0 && tree.Within(p.ParentID, p.ID) {
		errs = append(errs, "A place can't go inside itself.")
	} else if !tree.CanHold(p.ParentID, p.Kind) {
		parent, _ := tree.Get(p.ParentID)
		errs = append(errs, fmt.Sprintf("%s can't go inside %s. %s", capFirst(aKind(p.Kind)), aKind(parent.Kind), fitsHint(p.Kind)))
	}
	for _, other := range p.AlsoIn {
		place, ok := tree.Get(other)
		switch {
		case !ok:
			errs = append(errs, "One of the other places it's in no longer exists.")
		case p.ID != 0 && tree.Within(other, p.ID):
			errs = append(errs, fmt.Sprintf("%s is inside this area, so the area can't also be in it.", place.Label()))
		case !store.KindFits(place.Kind, p.Kind):
			errs = append(errs, fmt.Sprintf("%s can't go inside %s.", capFirst(aKind(p.Kind)), aKind(place.Kind)))
		}
	}
	if p.ID != 0 {
		for _, child := range tree.Children(p.ID) {
			if !store.KindFits(p.Kind, child.Kind) {
				errs = append(errs, fmt.Sprintf("%s (%s) is inside it, and %s can't hold %s. Move what's inside first.",
					child.Label(), aKind(child.Kind), aKind(p.Kind), aKind(child.Kind)))
				break
			}
		}
	}
	return errs
}

// fitsHint says where a kind of place can go: "It can go inside a
// building, floor or area."
func fitsHint(kind string) string {
	parents := store.ParentKinds(kind)
	if len(parents) == 0 {
		return ""
	}
	list := parents[0]
	for i, k := range parents[1:] {
		if i == len(parents)-2 {
			list += " or " + k
		} else {
			list += ", " + k
		}
	}
	return "It can go inside " + aKind(list) + "."
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// aKind renders "a room" or "an area".
func aKind(kind string) string {
	if strings.ContainsAny(kind[:1], "aeiou") {
		return "an " + kind
	}
	return "a " + kind
}

func (s *Server) handlePlaceNew(w http.ResponseWriter, r *http.Request) {
	f := placeForm{Place: store.Place{ParentID: queryInt(r, "parent"), Kind: r.URL.Query().Get("kind")}}
	if f.Kind == "" {
		parent, _ := s.store.GetPlace(f.ParentID)
		if parent == nil {
			parent = &store.Place{}
		}
		f.Kind = defaultKind(parent.Kind)
	}
	s.renderPlaceForm(w, r, http.StatusOK, "Add a place", f, nil)
}

func (s *Server) handlePlaceCreate(w http.ResponseWriter, r *http.Request) {
	var f placeForm
	if errs := s.placeFromForm(r, &f); errs != nil {
		s.renderPlaceForm(w, r, http.StatusUnprocessableEntity, "Add a place", f, errs)
		return
	}
	if err := s.store.SavePlace(&f.Place); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/places/%d", f.ID), f.Label()+" added.")
}

func (s *Server) handlePlaceShow(w http.ResponseWriter, r *http.Request) {
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p, ok := tree.Get(pathID(r))
	if !ok {
		s.notFound(w, r)
		return
	}
	all := r.URL.Query().Get("all") == "1" && p.ItemTotal > p.ItemCount
	items, err := s.store.ListItems(store.ItemFilter{PlaceID: p.ID, Direct: !all})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	supplies, err := s.store.ListSupplies(store.SupplyFilter{PlaceID: p.ID})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	problems, err := s.store.ListProblems(store.ProblemFilter{PlaceID: p.ID, Status: "active"})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var tasks []store.Task
	if p.ChildCount > 0 {
		if tasks, err = s.store.ListTasks(store.TaskFilter{PlaceID: p.ID, ActiveOnly: true}); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	var parent store.Place
	if p.ParentID != 0 {
		parent, _ = tree.Get(p.ParentID)
	}
	s.render(w, r, http.StatusOK, "places/show", map[string]any{
		"Title": p.Label(), "Place": p, "Parent": parent, "Crumbs": tree.Ancestors(p.ID), "Children": tree.Children(p.ID), "AlsoIn": tree.AlsoIn(p.ID),
		"Items": items, "ItemGroups": groupItems(items), "AllItems": all, "Supplies": supplies, "Problems": problems,
		"Tasks": s.groupTasks(tasks), "SupplyLocations": len(tree.Inside(p.ID)) > 0,
	})
}

func (s *Server) handlePlaceEdit(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetPlace(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPlaceForm(w, r, http.StatusOK, "Edit "+p.Label(), placeForm{Place: *p, TagText: p.TagList()}, nil)
}

func (s *Server) handlePlaceUpdate(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetPlace(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f := placeForm{Place: *p}
	if errs := s.placeFromForm(r, &f); errs != nil {
		s.renderPlaceForm(w, r, http.StatusUnprocessableEntity, "Edit "+p.Label(), f, errs)
		return
	}
	if err := s.store.SavePlace(&f.Place); err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := "Saved."
	if f.ParentID != p.ParentID {
		msg = "Moved, along with everything in it."
	}
	s.redirect(w, r, fmt.Sprintf("/places/%d", p.ID), msg)
}

func (s *Server) handlePlaceDelete(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetPlace(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	err = s.store.DeletePlace(p.ID)
	var stuck *store.StuckError
	if errors.As(err, &stuck) {
		s.setFlash(w, r, "error", fmt.Sprintf("%s can't be deleted yet: %s (%s) is inside it and can't go straight inside %s. Move %s first.",
			p.Label(), stuck.Place.Label(), aKind(stuck.Place.Kind), stuck.Into.Label(), stuck.Place.Label()))
		http.Redirect(w, r, fmt.Sprintf("/places/%d", p.ID), http.StatusSeeOther)
		return
	} else if errors.Is(err, store.ErrPlaceNotEmpty) {
		s.setFlash(w, r, "error", p.Label()+" still has things in it. Move or delete them first.")
		http.Redirect(w, r, fmt.Sprintf("/places/%d", p.ID), http.StatusSeeOther)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	if p.ParentID == 0 {
		s.redirect(w, r, "/places", p.Label()+" deleted.")
		return
	}
	s.redirect(w, r, fmt.Sprintf("/places/%d", p.ParentID), p.Label()+" deleted. Anything that was in it is now here.")
}

// Bulk add: one place per line, as "Name" or "Number, Name" (a tab works
// too, so rows can be pasted straight from a spreadsheet).

type bulkPlacesForm struct {
	ParentID int64
	Kind     string
	Lines    string
}

func (s *Server) renderBulkForm(w http.ResponseWriter, r *http.Request, status int, f bulkPlacesForm, errs []string) {
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "places/bulk", map[string]any{
		"Title": "Add several places", "Form": f, "Parents": tree.All(), "Kinds": store.PlaceKinds[1:], "Errors": errs,
	})
}

// parseBulkPlaces turns the textarea into places, skipping blank lines.
func parseBulkPlaces(f bulkPlacesForm) ([]store.Place, []string) {
	var places []store.Place
	var errs []string
	for n, line := range strings.Split(f.Lines, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p := store.Place{ParentID: f.ParentID, Kind: f.Kind, Name: line}
		if i := strings.IndexAny(line, ",\t"); i >= 0 {
			p.Number, p.Name = strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		}
		if p.Name == "" {
			errs = append(errs, fmt.Sprintf("Line %d: a name is required.", n+1))
			continue
		}
		places = append(places, p)
	}
	if len(places) == 0 && len(errs) == 0 {
		errs = append(errs, "Enter at least one name.")
	}
	return places, errs
}

func (s *Server) handlePlaceBulkNew(w http.ResponseWriter, r *http.Request) {
	f := bulkPlacesForm{ParentID: queryInt(r, "parent"), Kind: store.KindRoom}
	if parent, err := s.store.GetPlace(f.ParentID); err == nil {
		f.Kind = defaultKind(parent.Kind)
	}
	s.renderBulkForm(w, r, http.StatusOK, f, nil)
}

func (s *Server) handlePlaceBulkCreate(w http.ResponseWriter, r *http.Request) {
	f := bulkPlacesForm{ParentID: formInt(r, "parent_id"), Kind: formStr(r, "kind"), Lines: r.PostFormValue("lines")}
	places, errs := parseBulkPlaces(f)
	check := store.Place{ParentID: f.ParentID, Kind: f.Kind}
	if f.Kind == store.KindSite {
		check.Kind = ""
	}
	errs = append(s.checkPlacement(&check), errs...)
	if errs != nil {
		s.renderBulkForm(w, r, http.StatusUnprocessableEntity, f, errs)
		return
	}
	for i := range places {
		places[i].ParentID = check.ParentID
	}
	if err := s.store.AddPlaces(places); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/places/%d", check.ParentID), pluralize(len(places), f.Kind)+" added.")
}

// Old links ------------------------------------------------------------------

// handleLegacy sends /buildings/3 and /rooms/7 (and their QR pages) to the
// places they became, so bookmarks keep working.
func (s *Server) handleLegacy(room bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "" {
			http.Redirect(w, r, "/places", http.StatusMovedPermanently)
			return
		}
		var id int64
		var err error
		if room {
			id, err = s.store.LegacyPlace(0, pathID(r))
		} else {
			id, err = s.store.LegacyPlace(pathID(r), 0)
		}
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		to := fmt.Sprintf("/places/%d", id)
		if strings.HasSuffix(r.URL.Path, "/qr") {
			to += "/qr"
			if !room {
				to += "?inside=1"
			}
		}
		http.Redirect(w, r, to, http.StatusMovedPermanently)
	}
}

// Pickers --------------------------------------------------------------------

// itemOption is an item in a "What's it about?" list, with the places it's
// in so the list can follow the chosen place.
type itemOption struct {
	ID      int64
	Label   string
	Lineage string       // "12 4 1": its place and every place that's in
	Units   []store.Unit // to say which one, on problem forms
}

func itemOptions(tree *store.Places, items []store.Item) []itemOption {
	out := make([]itemOption, len(items))
	for i, it := range items {
		ids := tree.Lineage(it.PlaceID)
		parts := make([]string, len(ids))
		for j, id := range ids {
			parts[j] = fmt.Sprint(id)
		}
		out[i] = itemOption{ID: it.ID, Label: it.Name + " (" + it.Place.Label() + ")", Lineage: strings.Join(parts, " ")}
	}
	return out
}

// filterPlaces are the places offered to narrow a list or dashboard. When
// short is set, rooms and the areas inside them are left out to keep the
// list manageable.
func filterPlaces(tree *store.Places, short bool) []store.Place {
	var out []store.Place
	for _, p := range tree.All() {
		parent, _ := tree.Get(p.ParentID)
		if !short || p.Kind != store.KindRoom && !(p.Kind == store.KindArea && (parent.Kind == store.KindRoom || parent.Kind == store.KindArea)) {
			out = append(out, p)
		}
	}
	return out
}
