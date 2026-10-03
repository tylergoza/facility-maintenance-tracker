package store

import (
	"cmp"
	"database/sql"
	"errors"
	"slices"
	"strings"
)

// Place kinds, from the top of the tree down. A place always sits under a
// higher level, but levels can be skipped: a porch is a zone straight
// under its building, a closet off a hallway is a room in a zone.
const (
	KindSite     = "site"
	KindBuilding = "building"
	KindFloor    = "floor"
	KindZone     = "zone"
	KindRoom     = "room"
	KindArea     = "area"
)

// PlaceKinds lists every kind, top level first.
var PlaceKinds = []string{KindSite, KindBuilding, KindFloor, KindZone, KindRoom, KindArea}

// KindLevel is 0 for a site up to 5 for an area; -1 for an unknown kind.
func KindLevel(kind string) int { return slices.Index(PlaceKinds, kind) }

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
		return "A property with its own address: the whole campus, its grounds, mailbox and parking lot."
	case KindBuilding:
		return "A building on the site."
	case KindFloor:
		return "A level of a building."
	case KindZone:
		return "A wing or group of rooms, or a space without walls: a hallway, a porch."
	case KindRoom:
		return "A room with walls and a door."
	case KindArea:
		return "Part of a room: a stage, a sound booth, a closet inside a room."
	}
	return ""
}

// ChildKinds are the kinds that can go inside a place of this kind.
func ChildKinds(kind string) []string {
	if l := KindLevel(kind); l >= 0 {
		return PlaceKinds[l+1:]
	}
	return nil
}

// ErrPlaceNotEmpty is returned when deleting a site that still has
// anything in it; there is nowhere above it to move things to.
var ErrPlaceNotEmpty = errors.New("place is not empty")

type Place struct {
	ID       int64
	ParentID int64 // 0 for sites
	Kind     string
	Number   string // optional, e.g. room "104"
	Name     string
	Address  string
	Notes    string
	Tags     []string

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
func (p Place) Level() int           { return KindLevel(p.Kind) }
func (p Place) ChildKinds() []string { return ChildKinds(p.Kind) }

// TagList renders the tags for a form field: "classrooms, media".
func (p Place) TagList() string { return strings.Join(p.Tags, ", ") }

// Places is the whole tree, loaded at once: a site has tens or hundreds of
// places, not millions.
type Places struct {
	list     []*Place // tree order, parents first
	byID     map[int64]*Place
	children map[int64][]*Place // by parent id; 0 holds the sites
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
	t := &Places{byID: map[int64]*Place{}, children: map[int64][]*Place{}}
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
	for _, p := range t.byID {
		t.children[p.ParentID] = append(t.children[p.ParentID], p)
	}
	for _, kids := range t.children {
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

// comparePlaces orders siblings: higher levels first (floors before rooms),
// then by number naturally (unnumbered last), then by name.
func comparePlaces(a, b *Place) int {
	return cmp.Or(
		cmp.Compare(KindLevel(a.Kind), KindLevel(b.Kind)),
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
	// Children come after their parents, so walking backwards adds each
	// place's totals to its parent after its own are complete.
	for _, p := range t.list {
		p.ItemTotal, p.ProblemTotal, p.ChildCount = p.ItemCount, p.ProblemCount, len(t.children[p.ID])
	}
	for i := len(t.list) - 1; i >= 0; i-- {
		p := t.list[i]
		if parent := t.byID[p.ParentID]; parent != nil {
			parent.ItemTotal += p.ItemTotal
			parent.ProblemTotal += p.ProblemTotal
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

// Children returns the places directly inside a place (0 for the sites).
func (t *Places) Children(id int64) []Place { return copyPlaces(t.children[id]) }

// Sites returns the top-level places.
func (t *Places) Sites() []Place { return t.Children(0) }

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

// Ancestors returns the places a place is inside, outermost first, leaving
// out a single site.
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

// Within reports whether id is the place outer or somewhere inside it.
func (t *Places) Within(id, outer int64) bool {
	for p := t.byID[id]; p != nil; p = t.byID[p.ParentID] {
		if p.ID == outer {
			return true
		}
	}
	return false
}

// Lineage returns a place's id and those of every place it's inside, for
// filtering lists in the browser.
func (t *Places) Lineage(id int64) []int64 {
	var out []int64
	for p := t.byID[id]; p != nil; p = t.byID[p.ParentID] {
		out = append(out, p.ID)
	}
	return out
}

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

// Inside returns everything inside a place, in tree order, not including it.
func (t *Places) Inside(id int64) []Place {
	var out []Place
	var walk func(int64)
	walk = func(parent int64) {
		for _, p := range t.children[parent] {
			out = append(out, *p)
			walk(p.ID)
		}
	}
	walk(id)
	return out
}

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
// the top). Only sites go at the top, and everything else goes inside a
// higher level.
func (t *Places) CanHold(parent int64, kind string) bool {
	if KindLevel(kind) < 0 {
		return false
	}
	if parent == 0 {
		return kind == KindSite
	}
	p := t.byID[parent]
	return p != nil && p.Level() < KindLevel(kind)
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

// DeletePlace removes a place. Whatever was inside it (places, items,
// supplies, problems) moves up to the place it was in. A site has nothing
// above it, so it can only be deleted once it's empty.
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

// inPlace is SQL matching col to a place (the next argument) or anything
// inside it.
func inPlace(col string) string {
	return col + ` IN (WITH RECURSIVE sub(id) AS (SELECT ? UNION SELECT p.id FROM places p JOIN sub ON p.parent_id = sub.id) SELECT id FROM sub)`
}

// inTag is SQL matching col to places with a tag (the next argument) or
// anything inside them.
func inTag(col string) string {
	return col + ` IN (WITH RECURSIVE sub(id) AS (
		SELECT pt.place_id FROM place_tags pt JOIN tags t ON t.id = pt.tag_id WHERE t.name = ?
		UNION SELECT p.id FROM places p JOIN sub ON p.parent_id = sub.id) SELECT id FROM sub)`
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
