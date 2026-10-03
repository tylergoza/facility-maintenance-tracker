package store

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Place kinds. Sites hold buildings; buildings hold floors, rooms and
// areas. Areas are any other space: outside (a porch, the parking lot), a
// wing or hallway holding floors or rooms, or part of a room (a stage).
const (
	KindSite     = "site"
	KindBuilding = "building"
	KindFloor    = "floor"
	KindRoom     = "room"
	KindArea     = "area"
)

// PlaceKinds lists every kind, outermost first.
var PlaceKinds = []string{KindSite, KindBuilding, KindFloor, KindRoom, KindArea}

// fitsIn lists the kinds each kind can go inside. Sites go at the top.
var fitsIn = map[string][]string{
	KindSite:     nil,
	KindBuilding: {KindSite},
	KindFloor:    {KindBuilding, KindArea},
	KindRoom:     {KindBuilding, KindFloor, KindArea},
	KindArea:     {KindSite, KindBuilding, KindFloor, KindRoom, KindArea},
}

// ValidKind reports whether kind is one of PlaceKinds.
func ValidKind(kind string) bool { return slices.Contains(PlaceKinds, kind) }

// KindFits reports whether a place of kind can go inside one of parentKind.
func KindFits(parentKind, kind string) bool { return slices.Contains(fitsIn[kind], parentKind) }

// ParentKinds are the kinds a place of this kind can go inside.
func ParentKinds(kind string) []string { return fitsIn[kind] }

// ChildKinds are the kinds that can go inside a place of this kind.
func ChildKinds(kind string) []string {
	var out []string
	for _, k := range PlaceKinds {
		if KindFits(kind, k) {
			out = append(out, k)
		}
	}
	return out
}

// KindLabel renders a kind for people: "Building".
func KindLabel(kind string) string {
	if kind == "" {
		return ""
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}

// KindHint explains a kind on the place form.
func KindHint(kind string) string {
	switch kind {
	case KindSite:
		return "A property with its own address: the whole campus, its grounds and parking lot."
	case KindBuilding:
		return "A building on the site."
	case KindFloor:
		return "A level of a building, or of a wing."
	case KindRoom:
		return "A room with walls and a door."
	case KindArea:
		return "Any other space: outside (a porch, a playground), a wing or hallway that holds rooms, or part of a room (a stage, a closet). A shared space like a stairwell can also be in more than one place."
	}
	return ""
}

// kindRank orders siblings: floors, then rooms, then areas.
var kindRank = map[string]int{KindSite: 0, KindBuilding: 1, KindFloor: 2, KindRoom: 3, KindArea: 4}

// ErrPlaceNotEmpty is returned when deleting a site that still has
// anything in it; there is nowhere above it to move things to.
var ErrPlaceNotEmpty = errors.New("place is not empty")

// StuckError is returned when deleting a place whose contents can't move
// up a level, e.g. a room in an area on the site (rooms don't go on sites).
type StuckError struct {
	Place Place // what's inside
	Into  Place // where it would have to go
}

func (e *StuckError) Error() string {
	return fmt.Sprintf("%s (%s) can't go straight inside %s", e.Place.Label(), e.Place.Kind, e.Into.Label())
}

type Place struct {
	ID       int64
	ParentID int64 // 0 for sites; for shared areas, the one it lives in
	Kind     string
	Number   string // optional, e.g. room "104"
	Name     string
	Address  string
	Notes    string
	Tags     []string
	AlsoIn   []int64 // areas only: other places a shared area is in

	// Filled in from the whole tree (see Places).
	Path      string // "Main Building › 2nd floor › 104 – Nursery"
	Depth     int    // levels below the top of what's shown
	Order     int    // position in the tree, parents first
	PostOrder int    // position in the tree, children first
	// Filled in by Store.Places: counts here and including everything inside.
	ItemCount, ItemTotal       int
	ProblemCount, ProblemTotal int // open and in progress
	ChildCount                 int
}

// Label renders "104 – Nursery", or just the name when there is no number.
func (p Place) Label() string { return RoomLabel(p.Number, p.Name) }

func (p Place) KindLabel() string    { return KindLabel(p.Kind) }
func (p Place) ChildKinds() []string { return ChildKinds(p.Kind) }

// Shared reports whether it's also in places other than its own.
func (p Place) Shared() bool { return len(p.AlsoIn) > 0 }

// TagList renders the tags for a form field: "classrooms, media".
func (p Place) TagList() string { return strings.Join(p.Tags, ", ") }

// Places is the whole tree, loaded at once: a site has tens or hundreds of
// places, not millions. Each place has one parent; shared areas are also
// linked into other places.
type Places struct {
	list     []*Place // tree order, parents first
	byID     map[int64]*Place
	children map[int64][]*Place // by parent id; 0 holds the sites
	linked   map[int64][]*Place // shared areas also in a place, by that place's id
}

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// loadPlaces reads the tree without tags or counts.
func loadPlaces(q querier) (*Places, error) {
	rows, err := q.Query(`SELECT id, COALESCE(parent_id, 0), kind, number, name, address, notes FROM places`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	t := &Places{byID: map[int64]*Place{}, children: map[int64][]*Place{}, linked: map[int64][]*Place{}}
	for rows.Next() {
		p := &Place{}
		if err := rows.Scan(&p.ID, &p.ParentID, &p.Kind, &p.Number, &p.Name, &p.Address, &p.Notes); err != nil {
			return nil, err
		}
		t.byID[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	links, err := q.Query(`SELECT place_id, parent_id FROM place_links ORDER BY parent_id`)
	if err != nil {
		return nil, err
	}
	defer links.Close()
	for links.Next() {
		var id, parent int64
		if err := links.Scan(&id, &parent); err != nil {
			return nil, err
		}
		if p, other := t.byID[id], t.byID[parent]; p != nil && other != nil {
			p.AlsoIn = append(p.AlsoIn, parent)
			t.linked[parent] = append(t.linked[parent], p)
		}
	}
	if err := links.Err(); err != nil {
		return nil, err
	}
	for _, p := range t.byID {
		t.children[p.ParentID] = append(t.children[p.ParentID], p)
	}
	for _, kids := range t.children {
		slices.SortFunc(kids, comparePlaces)
	}
	for _, kids := range t.linked {
		slices.SortFunc(kids, comparePlaces)
	}
	// With one site, paths and lists start at its buildings.
	hidden := int64(0)
	if sites := t.children[0]; len(sites) == 1 {
		hidden = sites[0].ID
	}
	post := 0
	var walk func(parent int64, prefix string, depth int)
	walk = func(parent int64, prefix string, depth int) {
		for _, p := range t.children[parent] {
			p.Order, p.Depth = len(t.list), depth
			t.list = append(t.list, p)
			p.Path = p.Label()
			if prefix != "" {
				p.Path = prefix + " › " + p.Label()
			}
			childPrefix, childDepth := p.Path, depth+1
			if p.ID == hidden {
				childPrefix, childDepth = "", depth
			}
			walk(p.ID, childPrefix, childDepth)
			p.PostOrder = post
			post++
		}
	}
	walk(0, "", 0)
	return t, nil
}

// comparePlaces orders siblings: floors, then rooms, then areas; then by
// number naturally (unnumbered last), then by name.
func comparePlaces(a, b *Place) int {
	return cmp.Or(
		cmp.Compare(kindRank[a.Kind], kindRank[b.Kind]),
		CompareRoomNumbers(a.Number, b.Number),
		CompareRoomNumbers(a.Name, b.Name),
		cmp.Compare(a.ID, b.ID),
	)
}

// Places loads the whole tree with tags and item and problem counts.
func (s *Store) Places() (*Places, error) {
	t, err := loadPlaces(s.DB)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT pt.place_id, t.name FROM place_tags pt JOIN tags t ON t.id = pt.tag_id ORDER BY t.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var tag string
		if err := rows.Scan(&id, &tag); err != nil {
			rows.Close()
			return nil, err
		}
		if p := t.byID[id]; p != nil {
			p.Tags = append(p.Tags, tag)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	counts := []struct {
		q   string
		set func(p *Place, n int)
	}{
		{`SELECT place_id, COUNT(*) FROM items GROUP BY place_id`, func(p *Place, n int) { p.ItemCount = n }},
		{`SELECT place_id, COUNT(*) FROM problems WHERE status != 'resolved' GROUP BY place_id`, func(p *Place, n int) { p.ProblemCount = n }},
	}
	for _, c := range counts {
		rows, err := s.DB.Query(c.q)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return nil, err
			}
			if p := t.byID[id]; p != nil {
				c.set(p, n)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	// Totals count everything inside once, even a shared area reachable
	// two ways.
	for _, p := range t.list {
		p.ItemTotal, p.ProblemTotal = p.ItemCount, p.ProblemCount
		p.ChildCount = len(t.children[p.ID]) + len(t.linked[p.ID])
		for _, in := range t.inside(p.ID) {
			p.ItemTotal += in.ItemCount
			p.ProblemTotal += in.ProblemCount
		}
	}
	return t, nil
}

// Get returns a copy of a place, and false if there is no such place.
func (t *Places) Get(id int64) (Place, bool) {
	if p := t.byID[id]; p != nil {
		return *p, true
	}
	return Place{}, false
}

// Path renders where a place is, or "" for an unknown id.
func (t *Places) Path(id int64) string {
	if p := t.byID[id]; p != nil {
		return p.Path
	}
	return ""
}

// All returns every place in tree order, parents first.
func (t *Places) All() []Place { return copyPlaces(t.list) }

// Len is how many places there are.
func (t *Places) Len() int { return len(t.list) }

// Children returns the places directly inside a place (0 for the sites):
// its own, then shared areas that are also in it (their ParentID is
// elsewhere).
func (t *Places) Children(id int64) []Place {
	return append(copyPlaces(t.children[id]), copyPlaces(t.linked[id])...)
}

// AlsoIn returns the other places a shared area is in.
func (t *Places) AlsoIn(id int64) []Place {
	var out []Place
	if p := t.byID[id]; p != nil {
		for _, other := range p.AlsoIn {
			out = append(out, *t.byID[other])
		}
	}
	slices.SortFunc(out, func(a, b Place) int { return cmp.Compare(a.Order, b.Order) })
	return out
}

// Sites returns the top-level places.
func (t *Places) Sites() []Place { return copyPlaces(t.children[0]) }

// SingleSite returns the only site when there is exactly one. It's left
// out of paths and lists, so a one-site setup never has to think about it.
func (t *Places) SingleSite() (Place, bool) {
	if sites := t.children[0]; len(sites) == 1 {
		return *sites[0], true
	}
	return Place{}, false
}

// Top returns the places shown at the top of lists: the sites, or with a
// single site, what's in it.
func (t *Places) Top() []Place {
	if site, ok := t.SingleSite(); ok {
		return t.Children(site.ID)
	}
	return t.Sites()
}

// Ancestors returns the places a place lives inside, outermost first,
// leaving out a single site.
func (t *Places) Ancestors(id int64) []Place {
	var out []Place
	single, _ := t.SingleSite()
	p := t.byID[id]
	for p != nil {
		if parent := t.byID[p.ParentID]; parent != nil && parent.ID != single.ID {
			out = append(out, *parent)
		}
		p = t.byID[p.ParentID]
	}
	slices.Reverse(out)
	return out
}

// up returns a place and every place it's in, through shared links too.
func (t *Places) up(id int64) []int64 {
	var out []int64
	seen := map[int64]bool{}
	queue := []int64{id}
	for len(queue) > 0 {
		p := t.byID[queue[0]]
		queue = queue[1:]
		if p == nil || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		out = append(out, p.ID)
		queue = append(append(queue, p.ParentID), p.AlsoIn...)
	}
	return out
}

// Within reports whether id is the place outer or somewhere inside it,
// counting shared areas as inside every place they're in.
func (t *Places) Within(id, outer int64) bool { return slices.Contains(t.up(id), outer) }

// Lineage returns a place's id and those of every place it's in, for
// filtering lists in the browser.
func (t *Places) Lineage(id int64) []int64 { return t.up(id) }

// Building returns the building a place is in (or is), or the site for
// places outside any building.
func (t *Places) Building(id int64) (Place, bool) {
	var site *Place
	for p := t.byID[id]; p != nil; p = t.byID[p.ParentID] {
		switch p.Kind {
		case KindBuilding:
			return *p, true
		case KindSite:
			site = p
		}
	}
	if site != nil {
		return *site, true
	}
	return Place{}, false
}

// inside returns everything inside a place, each once, in tree order, not
// including it.
func (t *Places) inside(id int64) []*Place {
	var out []*Place
	seen := map[int64]bool{id: true}
	var walk func(int64)
	walk = func(parent int64) {
		for _, kids := range [][]*Place{t.children[parent], t.linked[parent]} {
			for _, p := range kids {
				if !seen[p.ID] {
					seen[p.ID] = true
					out = append(out, p)
					walk(p.ID)
				}
			}
		}
	}
	walk(id)
	slices.SortFunc(out, func(a, b *Place) int { return cmp.Compare(a.Order, b.Order) })
	return out
}

// Inside returns everything inside a place, in tree order, not including it.
func (t *Places) Inside(id int64) []Place { return copyPlaces(t.inside(id)) }

// Tagged returns places with a tag, in tree order.
func (t *Places) Tagged(tag string) []Place {
	var out []Place
	for _, p := range t.list {
		if slices.ContainsFunc(p.Tags, func(x string) bool { return strings.EqualFold(x, tag) }) {
			out = append(out, *p)
		}
	}
	return out
}

// TagCount is a tag and how many places have it.
type TagCount struct {
	Name  string
	Count int
}

// Tags returns every tag in use, alphabetically.
func (t *Places) Tags() []TagCount {
	var out []TagCount
	for _, p := range t.list {
		for _, tag := range p.Tags {
			i := slices.IndexFunc(out, func(c TagCount) bool { return strings.EqualFold(c.Name, tag) })
			if i < 0 {
				out = append(out, TagCount{Name: tag})
				i = len(out) - 1
			}
			out[i].Count++
		}
	}
	slices.SortFunc(out, func(a, b TagCount) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

// CanHold reports whether a place of kind can go inside parent (0 = at
// the top, which only sites can).
func (t *Places) CanHold(parent int64, kind string) bool {
	if !ValidKind(kind) {
		return false
	}
	if parent == 0 {
		return kind == KindSite
	}
	p := t.byID[parent]
	return p != nil && KindFits(p.Kind, kind)
}

func copyPlaces(ps []*Place) []Place {
	out := make([]Place, len(ps))
	for i, p := range ps {
		out[i] = *p
	}
	return out
}

// Saving -------------------------------------------------------------------

// GetPlace returns one place with its tags and path.
func (s *Store) GetPlace(id int64) (*Place, error) {
	t, err := s.Places()
	if err != nil {
		return nil, err
	}
	p, ok := t.Get(id)
	if !ok {
		return nil, ErrNotFound
	}
	return &p, nil
}

// SavePlace creates or updates a place and its tags. Moving a place to a
// new parent takes everything inside it along. Callers check the tree
// rules first (see Places.CanHold).
func (s *Store) SavePlace(p *Place) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := savePlaceTx(tx, p); err != nil {
		return err
	}
	return tx.Commit()
}

// AddPlaces inserts several new places in one transaction: all or nothing.
func (s *Store) AddPlaces(places []Place) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := range places {
		if err := savePlaceTx(tx, &places[i]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func savePlaceTx(tx *sql.Tx, p *Place) error {
	p.Name, p.Number = strings.TrimSpace(p.Name), strings.TrimSpace(p.Number)
	args := []any{nullInt(p.ParentID), p.Kind, p.Number, p.Name, p.Address, p.Notes}
	if p.ID == 0 {
		res, err := tx.Exec(`INSERT INTO places (parent_id, kind, number, name, address, notes) VALUES (?, ?, ?, ?, ?, ?)`, args...)
		if err != nil {
			return err
		}
		if p.ID, err = res.LastInsertId(); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`UPDATE places SET parent_id = ?, kind = ?, number = ?, name = ?, address = ?, notes = ? WHERE id = ?`, append(args, p.ID)...); err != nil {
		return err
	}
	// Only areas can be shared, and never with the place they live in.
	if _, err := tx.Exec(`DELETE FROM place_links WHERE place_id = ?`, p.ID); err != nil {
		return err
	}
	if p.Kind != KindArea {
		p.AlsoIn = nil
	}
	p.AlsoIn = slices.DeleteFunc(slices.Compact(slices.Sorted(slices.Values(p.AlsoIn))), func(id int64) bool { return id == p.ParentID || id == p.ID })
	for _, other := range p.AlsoIn {
		if _, err := tx.Exec(`INSERT INTO place_links (place_id, parent_id) VALUES (?, ?)`, p.ID, other); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM place_tags WHERE place_id = ?`, p.ID); err != nil {
		return err
	}
	for _, tag := range p.Tags {
		if _, err := tx.Exec(`INSERT INTO tags (name) VALUES (?) ON CONFLICT (name) DO NOTHING`, tag); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO place_tags (place_id, tag_id) SELECT ?, id FROM tags WHERE name = ?`, p.ID, tag); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`DELETE FROM tags WHERE id NOT IN (SELECT tag_id FROM place_tags)`)
	return err
}

// ParseTags splits "Classrooms, media" into tags, tidying spaces and
// dropping blanks and repeats (ignoring case).
func ParseTags(s string) []string {
	var out []string
	for _, tag := range strings.Split(s, ",") {
		tag = strings.Join(strings.Fields(tag), " ")
		if tag != "" && !slices.ContainsFunc(out, func(x string) bool { return strings.EqualFold(x, tag) }) {
			out = append(out, tag)
		}
	}
	return out
}

// DeletePlace removes a place. Whatever lived inside it (places, items,
// supplies, problems) moves up to the place it was in; shared areas that
// were only linked to it just lose the link. A site has nothing above it,
// so it can only be deleted once it's empty. If something inside can't go
// in the place above (a room in an area on the site), it returns a
// *StuckError and changes nothing.
func (s *Store) DeletePlace(id int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var parent int64
	if err := tx.QueryRow(`SELECT COALESCE(parent_id, 0) FROM places WHERE id = ?`, id).Scan(&parent); err != nil {
		return notFound(err)
	}
	if parent == 0 {
		var n int
		if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM places WHERE parent_id = ?) + (SELECT COUNT(*) FROM items WHERE place_id = ?)
			+ (SELECT COUNT(*) FROM supplies WHERE place_id = ?) + (SELECT COUNT(*) FROM problems WHERE place_id = ?)`, id, id, id, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrPlaceNotEmpty
		}
	} else {
		t, err := loadPlaces(tx)
		if err != nil {
			return err
		}
		into, _ := t.Get(parent)
		for _, child := range t.children[id] {
			if !KindFits(into.Kind, child.Kind) {
				return &StuckError{Place: *child, Into: into}
			}
		}
		// A shared area moving up into a place it was also linked to now
		// just lives there.
		if _, err := tx.Exec(`DELETE FROM place_links WHERE parent_id = ? AND place_id IN (SELECT id FROM places WHERE parent_id = ?)`, parent, id); err != nil {
			return err
		}
		for _, q := range []string{
			`UPDATE places SET parent_id = ? WHERE parent_id = ?`,
			`UPDATE items SET place_id = ? WHERE place_id = ?`,
			`UPDATE supplies SET place_id = ? WHERE place_id = ?`,
			`UPDATE problems SET place_id = ? WHERE place_id = ?`,
		} {
			if _, err := tx.Exec(q, parent, id); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`DELETE FROM places WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tags WHERE id NOT IN (SELECT tag_id FROM place_tags)`); err != nil {
		return err
	}
	return tx.Commit()
}

// EnsureSite returns the site new buildings go in when nobody has picked
// one: the only site, created (named name) if there are none yet. It
// returns 0 when there are several sites to choose from.
func (s *Store) EnsureSite(name string) (int64, error) {
	rows, err := s.DB.Query(`SELECT id FROM places WHERE parent_id IS NULL LIMIT 2`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	switch len(ids) {
	case 0:
		site := &Place{Kind: KindSite, Name: name}
		err := s.SavePlace(site)
		return site.ID, err
	case 1:
		return ids[0], nil
	}
	return 0, nil
}

// LegacyPlace finds the place a building or room became when places
// replaced them, for old links and printed QR codes. Pass one of the two.
func (s *Store) LegacyPlace(buildingID, roomID int64) (int64, error) {
	var id int64
	var err error
	switch {
	case roomID != 0:
		err = s.DB.QueryRow(`SELECT id FROM places WHERE room_id = ?`, roomID).Scan(&id)
	case buildingID != 0:
		err = s.DB.QueryRow(`SELECT id FROM places WHERE building_id = ?`, buildingID).Scan(&id)
	default:
		return 0, ErrNotFound
	}
	return id, notFound(err)
}

// SQL helpers ----------------------------------------------------------------

// placeEdges is every "is inside" link: each place's own parent, and the
// other places shared areas are in.
const placeEdges = `(SELECT id AS child, parent_id AS parent FROM places UNION ALL SELECT place_id, parent_id FROM place_links)`

// inPlace is SQL matching col to a place (the next argument) or anything
// inside it, shared areas included.
func inPlace(col string) string {
	return col + ` IN (WITH RECURSIVE sub(id) AS (SELECT ? UNION SELECT e.child FROM ` + placeEdges + ` e JOIN sub ON e.parent = sub.id) SELECT id FROM sub)`
}

// inTag is SQL matching col to places with a tag (the next argument) or
// anything inside them.
func inTag(col string) string {
	return col + ` IN (WITH RECURSIVE sub(id) AS (
		SELECT pt.place_id FROM place_tags pt JOIN tags t ON t.id = pt.tag_id WHERE t.name = ?
		UNION SELECT e.child FROM ` + placeEdges + ` e JOIN sub ON e.parent = sub.id) SELECT id FROM sub)`
}

// placeFilter adds "in this place" or "in places with this tag" to a
// query. direct limits it to things in the place itself.
func placeFilter(q string, args []any, col string, placeID int64, direct bool, tag string) (string, []any) {
	switch {
	case placeID != 0 && direct:
		q += ` AND ` + col + ` = ?`
		args = append(args, placeID)
	case placeID != 0:
		q += ` AND ` + inPlace(col)
		args = append(args, placeID)
	}
	if tag != "" {
		q += ` AND ` + inTag(col)
		args = append(args, tag)
	}
	return q, args
}
