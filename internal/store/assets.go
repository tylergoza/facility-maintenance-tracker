package store

import (
	"cmp"
	"database/sql"
	"slices"
	"strings"
)

type Item struct {
	ID           int64
	PlaceID      int64
	Place        Place // where it is, with its path
	Name         string
	Category     string
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

// SupplyTotal is how many of the supply it takes to replace every one.
func (i Item) SupplyTotal() int { return i.Quantity * i.SupplyPer }

// SupplyShort reports whether there isn't enough on hand to replace one.
func (i Item) SupplyShort() bool { return i.SupplyID != 0 && i.Supply.Quantity < i.SupplyPer }

// Location renders where the item is: "Main Building › 104 – Nursery".
func (i Item) Location() string { return i.Place.Path }

// Items ------------------------------------------------------------------

type ItemFilter struct {
	PlaceID  int64 // in this place or anywhere inside it
	Direct   bool  // only items in PlaceID itself
	Tag      string
	SupplyID int64
	Query    string
}

const itemSelect = `
	SELECT i.id, i.place_id, i.name, i.category, i.manufacturer, i.model, i.serial_number,
	       COALESCE(i.install_date, ''), i.notes, i.quantity, COALESCE(i.supply_id, 0), i.supply_per, i.portable,
	       COALESCE(s.name, ''), COALESCE(s.unit, ''), COALESCE(s.quantity, 0), COALESCE(s.reorder_at, 0), COALESCE(s.place_id, 0),
	       COALESCE((SELECT MIN(t.next_due_on) FROM tasks t WHERE t.item_id = i.id AND t.active = 1), ''),
	       COALESCE((SELECT MAX(l.performed_on) FROM maintenance_logs l WHERE l.item_id = i.id AND l.replaced IS NOT NULL), '')
	FROM items i
	LEFT JOIN supplies s ON s.id = i.supply_id`

// scanItems reads items and fills in where they and their supplies are,
// in tree order then by name.
func (s *Store) scanItems(rows *sql.Rows) ([]Item, error) {
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var i Item
		if err := rows.Scan(&i.ID, &i.PlaceID, &i.Name, &i.Category, &i.Manufacturer, &i.Model, &i.SerialNumber, &i.InstallDate, &i.Notes,
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
	if f.Query != "" {
		like := "%" + f.Query + "%"
		q += ` AND (i.name LIKE ? OR i.category LIKE ? OR i.manufacturer LIKE ? OR i.model LIKE ? OR i.serial_number LIKE ?)`
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

// SaveItem creates or updates an item's details. When newSupply is given
// it is added to supplies first (in the item's building) and becomes the
// supply the item uses, in the same transaction. Location changes on an
// existing item should go through MoveItem so they're recorded.
func (s *Store) SaveItem(i *Item, newSupply *Supply, userID int64) error {
	i.Name, i.Category = strings.TrimSpace(i.Name), strings.TrimSpace(i.Category)
	i.Quantity, i.SupplyPer = max(i.Quantity, 1), max(i.SupplyPer, 1)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	args := []any{i.PlaceID, i.Name, i.Category, i.Manufacturer, i.Model, i.SerialNumber, nullStr(i.InstallDate), i.Notes,
		i.Quantity, nullInt(i.SupplyID), i.SupplyPer, i.Portable}
	if i.ID == 0 {
		res, err := tx.Exec(`INSERT INTO items (place_id, name, category, manufacturer, model, serial_number, install_date, notes,
			quantity, supply_id, supply_per, portable) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
		if err != nil {
			return err
		}
		if i.ID, err = res.LastInsertId(); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`UPDATE items SET place_id = ?, name = ?, category = ?, manufacturer = ?, model = ?,
		serial_number = ?, install_date = ?, notes = ?, quantity = ?, supply_id = ?, supply_per = ?, portable = ? WHERE id = ?`, append(args, i.ID)...); err != nil {
		return err
	}
	return tx.Commit()
}

// Move is a request to move an item to another place.
type Move struct {
	ItemID  int64
	PlaceID int64
	MovedOn string
	Note    string
	UserID  int64
}

// MoveItem changes an item's place and records the move in its history.
// Moving to where it already is records nothing.
func (s *Store) MoveItem(m Move) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from int64
	if err := tx.QueryRow(`SELECT place_id FROM items WHERE id = ?`, m.ItemID).Scan(&from); err != nil {
		return notFound(err)
	}
	if from == m.PlaceID {
		return nil
	}
	t, err := loadPlaces(tx)
	if err != nil {
		return err
	}
	if _, ok := t.Get(m.PlaceID); !ok {
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE items SET place_id = ? WHERE id = ?`, m.PlaceID, m.ItemID); err != nil {
		return err
	}
	notes := "Moved from " + t.Path(from) + " to " + t.Path(m.PlaceID) + "."
	if note := strings.TrimSpace(m.Note); note != "" {
		notes += "\n" + note
	}
	var by string
	_ = tx.QueryRow(`SELECT COALESCE(NULLIF(display_name, ''), username) FROM users WHERE id = ?`, m.UserID).Scan(&by)
	_, err = tx.Exec(`INSERT INTO maintenance_logs (item_id, kind, performed_on, performed_by, notes, created_by) VALUES (?, 'moved', ?, ?, ?, ?)`,
		m.ItemID, m.MovedOn, by, notes, nullInt(m.UserID))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteItem(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM items WHERE id = ?`, id)
	return err
}

// Categories returns distinct item categories for form autocompletion.
func (s *Store) Categories() ([]string, error) {
	rows, err := s.DB.Query(`SELECT DISTINCT category FROM items WHERE category != '' ORDER BY category COLLATE NOCASE`)
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
