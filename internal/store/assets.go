package store

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type Item struct {
	ID      int64
	PlaceID int64
	Place   Place // where it is, with its path
	// What it is. Name, Category and Counted come from its product; when
	// saving, Name picks the product (adding one with Category and
	// Counted if there's none by that name).
	ProductID    int64
	Name         string
	Category     string
	Counted      bool
	Manufacturer string
	Model        string
	SerialNumber string
	InstallDate  string
	Notes        string
	// How many identical ones this item counts, e.g. 10 outlets.
	Quantity int
	// Supply each one uses (SupplyID 0 = none), e.g. 1 bulb, and how many
	// of it each takes. Supply holds its current stock.
	SupplyID  int64
	SupplyPer int
	Supply    Supply
	// Portable items (projectors, TVs) get a Move action.
	Portable bool
	// Earliest due date across the item's active tasks ("" if none).
	NextDue string
	// Most recent date its supply was replaced ("" if never recorded).
	LastReplaced string
}

// HasUnits reports whether it has numbered units to pick from: there's
// more than one and its product isn't only counted.
func (i Item) HasUnits() bool { return !i.Counted && i.Quantity > 1 }

// SupplyTotal is how many of the supply it takes to replace every one.
func (i Item) SupplyTotal() int { return i.Quantity * i.SupplyPer }

// SupplyShort reports whether there isn't enough on hand to replace one.
func (i Item) SupplyShort() bool { return i.SupplyID != 0 && i.Supply.Quantity < i.SupplyPer }

// Location renders where the item is: "Main Building › 104 – Nursery".
func (i Item) Location() string { return i.Place.Path }

// Items ------------------------------------------------------------------

type ItemFilter struct {
	PlaceID      int64 // in this place or anywhere inside it
	Direct       bool  // only items in PlaceID itself
	Tag          string
	SupplyID     int64
	ProductID    int64
	PortableOnly bool
	Query        string
}

const itemSelect = `
	SELECT i.id, i.place_id, i.product_id, p.name, p.category, p.counted, i.manufacturer, i.model, i.serial_number,
	       COALESCE(i.install_date, ''), i.notes, i.quantity, COALESCE(i.supply_id, 0), i.supply_per, i.portable,
	       COALESCE(s.name, ''), COALESCE(s.unit, ''), COALESCE(s.quantity, 0), COALESCE(s.reorder_at, 0), COALESCE(s.place_id, 0),
	       COALESCE((SELECT MIN(t.next_due_on) FROM tasks t WHERE t.item_id = i.id AND t.active = 1), ''),
	       COALESCE((SELECT MAX(l.performed_on) FROM maintenance_logs l WHERE l.item_id = i.id AND l.replaced IS NOT NULL), '')
	FROM items i
	JOIN products p ON p.id = i.product_id
	LEFT JOIN supplies s ON s.id = i.supply_id`

// scanItems reads items and fills in where they and their supplies are,
// in tree order then by name.
func (s *Store) scanItems(rows *sql.Rows) ([]Item, error) {
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var i Item
		if err := rows.Scan(&i.ID, &i.PlaceID, &i.ProductID, &i.Name, &i.Category, &i.Counted, &i.Manufacturer, &i.Model, &i.SerialNumber, &i.InstallDate, &i.Notes,
			&i.Quantity, &i.SupplyID, &i.SupplyPer, &i.Portable,
			&i.Supply.Name, &i.Supply.Unit, &i.Supply.Quantity, &i.Supply.ReorderAt, &i.Supply.PlaceID, &i.NextDue, &i.LastReplaced); err != nil {
			return nil, err
		}
		i.Supply.ID = i.SupplyID
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	t, err := loadPlaces(s.DB)
	if err != nil {
		return nil, err
	}
	for k := range out {
		out[k].Place, _ = t.Get(out[k].PlaceID)
		out[k].Supply.Place, _ = t.Get(out[k].Supply.PlaceID)
	}
	slices.SortStableFunc(out, func(a, b Item) int {
		return cmp.Or(cmp.Compare(a.Place.Order, b.Place.Order), strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	return out, nil
}

func (s *Store) ListItems(f ItemFilter) ([]Item, error) {
	q := itemSelect + ` WHERE 1=1`
	var args []any
	q, args = placeFilter(q, args, "i.place_id", f.PlaceID, f.Direct, f.Tag)
	if f.SupplyID != 0 {
		q += ` AND i.supply_id = ?`
		args = append(args, f.SupplyID)
	}
	if f.ProductID != 0 {
		q += ` AND i.product_id = ?`
		args = append(args, f.ProductID)
	}
	if f.PortableOnly {
		q += ` AND i.portable = 1`
	}
	if f.Query != "" {
		like := "%" + f.Query + "%"
		q += ` AND (p.name LIKE ? OR p.category LIKE ? OR i.manufacturer LIKE ? OR i.model LIKE ? OR i.serial_number LIKE ?)`
		args = append(args, like, like, like, like, like)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return s.scanItems(rows)
}

func (s *Store) GetItem(id int64) (*Item, error) {
	rows, err := s.DB.Query(itemSelect+` WHERE i.id = ?`, id)
	if err != nil {
		return nil, err
	}
	is, err := s.scanItems(rows)
	if err != nil {
		return nil, err
	}
	if len(is) == 0 {
		return nil, ErrNotFound
	}
	return &is[0], nil
}

// SaveItem creates or updates an item's details. Its name picks its
// product, which is added if there's none by that name yet; a product no
// item is left in is removed. When newSupply is given
// it is added to supplies first (in the item's building) and becomes the
// supply the item uses, in the same transaction. Location changes on an
// existing item should go through MoveItem so they're recorded.
func (s *Store) SaveItem(i *Item, newSupply *Supply, userID int64) error {
	i.Quantity, i.SupplyPer = max(i.Quantity, 1), max(i.SupplyPer, 1)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := productFor(tx, i.Name, i.Category, i.Counted)
	if err != nil {
		return err
	}
	i.ProductID, i.Name, i.Category, i.Counted = p.ID, p.Name, p.Category, p.Counted
	if newSupply != nil {
		t, err := loadPlaces(tx)
		if err != nil {
			return err
		}
		building, ok := t.Building(i.PlaceID)
		if !ok {
			return ErrNotFound
		}
		newSupply.Name, newSupply.Unit = strings.TrimSpace(newSupply.Name), strings.TrimSpace(newSupply.Unit)
		newSupply.PlaceID, newSupply.Reusable = building.ID, false
		if err := insertSupplyTx(tx, newSupply, userID); err != nil {
			return err
		}
		i.SupplyID = newSupply.ID
	}
	args := []any{i.PlaceID, i.ProductID, i.Manufacturer, i.Model, i.SerialNumber, nullStr(i.InstallDate), i.Notes,
		i.Quantity, nullInt(i.SupplyID), i.SupplyPer, i.Portable}
	if i.ID == 0 {
		res, err := tx.Exec(`INSERT INTO items (place_id, product_id, manufacturer, model, serial_number, install_date, notes,
			quantity, supply_id, supply_per, portable) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
		if err != nil {
			return err
		}
		if i.ID, err = res.LastInsertId(); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`UPDATE items SET place_id = ?, product_id = ?, manufacturer = ?, model = ?,
		serial_number = ?, install_date = ?, notes = ?, quantity = ?, supply_id = ?, supply_per = ?, portable = ? WHERE id = ?`, append(args, i.ID)...); err != nil {
		return err
	}
	if err := refreshUnits(tx, `id = ?`, i.ID); err != nil {
		return err
	}
	if err := dropEmptyProducts(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Move is a request to move an item, or some of a group item, to another
// place.
type Move struct {
	ItemID  int64
	PlaceID int64
	// Which of a group item's units are going, or how many when they
	// aren't picked out (the highest-numbered go). Neither, or all of
	// them, moves the whole item.
	Units   []int
	Count   int
	MovedOn string
	Note    string
	UserID  int64
}

// MoveItem changes an item's place and records the move in its history.
// Moving to where it already is records nothing. It returns the item now
// holding what moved.
//
// Moving only some of a group item splits it: the ones moving join a
// matching item already at the new place (same product, make and supply), or
// else become a new item there with copies of its active tasks. The ones
// left behind are renumbered from #1 in order, and their location notes,
// history and problems follow them; problems about the moved ones go with
// them.
func (s *Store) MoveItem(m Move) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var from int64
	var quantity int
	var counted bool
	if err := tx.QueryRow(`SELECT i.place_id, i.quantity, p.counted FROM items i JOIN products p ON p.id = i.product_id WHERE i.id = ?`, m.ItemID).
		Scan(&from, &quantity, &counted); err != nil {
		return 0, notFound(err)
	}
	if counted {
		m.Units = nil // a count of them, not which ones
	}
	if from == m.PlaceID {
		return m.ItemID, nil
	}
	t, err := loadPlaces(tx)
	if err != nil {
		return 0, err
	}
	if _, ok := t.Get(m.PlaceID); !ok {
		return 0, ErrNotFound
	}
	moving, err := movingUnits(m, quantity)
	if err != nil {
		return 0, err
	}
	var by string
	_ = tx.QueryRow(`SELECT COALESCE(NULLIF(display_name, ''), username) FROM users WHERE id = ?`, m.UserID).Scan(&by)
	addLog := func(itemID int64, notes string) error {
		if note := strings.TrimSpace(m.Note); note != "" {
			notes += "\n" + note
		}
		_, err := tx.Exec(`INSERT INTO maintenance_logs (item_id, kind, performed_on, performed_by, notes, created_by) VALUES (?, 'moved', ?, ?, ?, ?)`,
			itemID, m.MovedOn, by, notes, nullInt(m.UserID))
		return err
	}
	fromPath, toPath := t.Path(from), t.Path(m.PlaceID)

	if moving == nil {
		if _, err := tx.Exec(`UPDATE items SET place_id = ? WHERE id = ?`, m.PlaceID, m.ItemID); err != nil {
			return 0, err
		}
		if err := addLog(m.ItemID, "Moved from "+fromPath+" to "+toPath+"."); err != nil {
			return 0, err
		}
		return m.ItemID, tx.Commit()
	}

	count, left := len(moving), quantity-len(moving)
	into, base, err := splitInto(tx, m.ItemID, m.PlaceID, count)
	if err != nil {
		return 0, err
	}
	// New numbers: those left behind close up from #1, those moving
	// follow on after whatever is already at the new place.
	stay, gone := map[int]int{}, map[int]int{}
	for n := 1; n <= quantity; n++ {
		if i := slices.Index(moving, n); i >= 0 {
			gone[n] = base + i + 1
		} else {
			stay[n] = len(stay) + 1
		}
	}
	if !counted {
		if err := renumberUnits(tx, m.ItemID, into, m.PlaceID, stay, gone); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(`UPDATE items SET quantity = ? WHERE id = ?`, left, m.ItemID); err != nil {
		return 0, err
	}

	notes := fmt.Sprintf("Moved %d of %d from %s to %s", count, quantity, fromPath, toPath)
	if counted {
		notes += "."
	} else {
		notes += ": " + UnitsLabel(moving) + "."
		if moving[0] <= left {
			notes += fmt.Sprintf(" The %d left are now %s, keeping their IDs.", left, unitRange(1, left))
		}
	}
	if err := addLog(m.ItemID, notes); err != nil {
		return 0, err
	}
	notes = fmt.Sprintf("Moved %d from %s to %s.", count, fromPath, toPath)
	if !counted {
		notes += fmt.Sprintf(" They were %s there and are %s here.", UnitsLabel(moving), unitRange(base+1, base+count))
	}
	if err := addLog(into, notes); err != nil {
		return 0, err
	}
	return into, tx.Commit()
}

// unitRef is a row (history entry, problem) that points at a unit.
type unitRef struct {
	id   int64
	unit int
}

// unitRefs runs a query selecting (id, unit) pairs.
func unitRefs(tx *sql.Tx, query string, args ...any) ([]unitRef, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []unitRef
	for rows.Next() {
		var r unitRef
		if err := rows.Scan(&r.id, &r.unit); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// movingUnits returns which units of a group item a move takes, or nil
// when it takes the whole item.
func movingUnits(m Move, quantity int) ([]int, error) {
	units := slices.Clone(m.Units)
	slices.Sort(units)
	units = slices.Compact(units)
	if err := checkUnits(units, quantity); err != nil {
		return nil, err
	}
	if len(units) == 0 && m.Count > 0 && m.Count < quantity {
		for n := quantity - m.Count + 1; n <= quantity; n++ {
			units = append(units, n)
		}
	}
	if len(units) == 0 || len(units) >= quantity {
		return nil, nil
	}
	return units, nil
}

// splitInto finds or makes the item at placeID that count of itemID are
// joining, and returns it with how many it already had.
func splitInto(tx *sql.Tx, itemID, placeID int64, count int) (into int64, base int, err error) {
	err = tx.QueryRow(`
		SELECT d.id, d.quantity FROM items i JOIN items d
		  ON d.place_id = ? AND d.id != i.id AND d.product_id = i.product_id
		 AND d.manufacturer = i.manufacturer AND d.model = i.model AND d.serial_number = i.serial_number
		 AND d.supply_id IS i.supply_id AND d.supply_per = i.supply_per AND d.portable = i.portable
		WHERE i.id = ? ORDER BY d.id LIMIT 1`, placeID, itemID).Scan(&into, &base)
	if err == nil {
		_, err = tx.Exec(`UPDATE items SET quantity = quantity + ? WHERE id = ?`, count, into)
		return into, base, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, err
	}
	res, err := tx.Exec(`
		INSERT INTO items (place_id, product_id, manufacturer, model, serial_number, install_date, notes,
		                   quantity, supply_id, supply_per, portable)
		SELECT ?, product_id, manufacturer, model, serial_number, install_date, notes, ?, supply_id, supply_per, portable
		FROM items WHERE id = ?`, placeID, count, itemID)
	if err != nil {
		return 0, 0, err
	}
	if into, err = res.LastInsertId(); err != nil {
		return 0, 0, err
	}
	_, err = tx.Exec(`
		INSERT INTO tasks (item_id, name, description, interval_value, interval_unit, last_completed_on, next_due_on, active,
		                   supply_id, supply_amount, supply_always)
		SELECT ?, name, description, interval_value, interval_unit, last_completed_on, next_due_on, active,
		       supply_id, supply_amount, supply_always
		FROM tasks WHERE item_id = ? AND active = 1 ORDER BY id`, into, itemID)
	return into, 0, err
}

// renumberUnits applies a split of itemID: units in stay keep their IDs,
// location notes, history and problems under new numbers; units in gone
// take their IDs and problems to the into item at placeID. A unit whose
// ID was its number keeps that number as its ID wherever it ends up. The
// notes of units that moved are dropped (they described where they were),
// as are their marks on the old item's history.
func renumberUnits(tx *sql.Tx, itemID, into, placeID int64, stay, gone map[int]int) error {
	old := map[int]Unit{}
	rows, err := tx.Query(`SELECT number, tag, label FROM item_units WHERE item_id = ?`, itemID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var u Unit
		if err := rows.Scan(&u.Number, &u.Tag, &u.Label); err != nil {
			rows.Close()
			return err
		}
		old[u.Number] = u
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM item_units WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for n := 1; n <= len(stay)+len(gone); n++ {
		u := old[n]
		u.Tag = unitID(n, u.Tag)
		if to, ok := stay[n]; ok {
			u.Number = to
			err = saveUnit(tx, itemID, u)
		} else {
			u.Number, u.Label = gone[n], ""
			err = saveUnit(tx, into, u)
		}
		if err != nil {
			return err
		}
	}

	marks, err := unitRefs(tx, `SELECT u.log_id, u.unit FROM log_units u JOIN maintenance_logs l ON l.id = u.log_id WHERE l.item_id = ?`, itemID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM log_units WHERE log_id IN (SELECT id FROM maintenance_logs WHERE item_id = ?)`, itemID); err != nil {
		return err
	}
	for _, u := range marks {
		if n, ok := stay[u.unit]; ok {
			if _, err := tx.Exec(`INSERT INTO log_units (log_id, unit) VALUES (?, ?)`, u.id, n); err != nil {
				return err
			}
		}
	}

	// Problems have no key on unit, so each can be updated in place.
	problems, err := unitRefs(tx, `SELECT id, unit FROM problems WHERE item_id = ? AND unit IS NOT NULL`, itemID)
	if err != nil {
		return err
	}
	for _, p := range problems {
		if n, ok := stay[p.unit]; ok {
			_, err = tx.Exec(`UPDATE problems SET unit = ? WHERE id = ?`, n, p.id)
		} else if n, ok := gone[p.unit]; ok {
			_, err = tx.Exec(`UPDATE problems SET item_id = ?, unit = ?, place_id = ? WHERE id = ?`, into, n, placeID, p.id)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// DeleteItem removes an item, and its product if it was the last of it.
func (s *Store) DeleteItem(id int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM items WHERE id = ?`, id); err != nil {
		return err
	}
	if err := dropEmptyProducts(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Categories returns distinct product categories for form autocompletion.
func (s *Store) Categories() ([]string, error) {
	rows, err := s.DB.Query(`SELECT DISTINCT category FROM products WHERE category != '' ORDER BY category COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Numbers -----------------------------------------------------------

// RoomLabel renders "104 – Nursery", or just the name when number is empty.
func RoomLabel(number, name string) string {
	if number == "" {
		return name
	}
	return number + " – " + name
}

// CompareRoomNumbers orders room numbers naturally ("9" < "10" < "B2" < "B10"),
// case-insensitively, with blank numbers last. Returns -1, 0 or 1.
func CompareRoomNumbers(a, b string) int {
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		ca, cb := a[0], b[0]
		if isDigit(ca) && isDigit(cb) {
			na, ra := splitDigits(a)
			nb, rb := splitDigits(b)
			na, nb = strings.TrimLeft(na, "0"), strings.TrimLeft(nb, "0")
			if len(na) != len(nb) {
				return cmp.Compare(len(na), len(nb))
			}
			if na != nb {
				return strings.Compare(na, nb)
			}
			a, b = ra, rb
			continue
		}
		if ca != cb {
			return cmp.Compare(ca, cb)
		}
		a, b = a[1:], b[1:]
	}
	return cmp.Compare(len(a), len(b))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func splitDigits(s string) (digits, rest string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}
