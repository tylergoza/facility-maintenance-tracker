package store

import (
	"cmp"
	"database/sql"
	"slices"
	"strings"
)

type Building struct {
	ID        int64
	Name      string
	Address   string
	Notes     string
	RoomCount int
	ItemCount int
}

type Room struct {
	ID           int64
	BuildingID   int64
	BuildingName string
	Number       string // optional room number, e.g. "104"
	Name         string
	Floor        string
	Notes        string
	ItemCount    int
}

// Label renders "104 – Nursery", or just the name when there is no number.
func (r Room) Label() string { return RoomLabel(r.Number, r.Name) }

type Item struct {
	ID           int64
	BuildingID   int64
	BuildingName string
	RoomID       int64 // 0 when the item is not in a specific room
	RoomNumber   string
	RoomName     string
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

// Location renders "Building › Room" or just the building name.
func (i Item) Location() string {
	if i.RoomName != "" {
		return i.BuildingName + " › " + RoomLabel(i.RoomNumber, i.RoomName)
	}
	return i.BuildingName
}

// Buildings --------------------------------------------------------------

func (s *Store) ListBuildings() ([]Building, error) {
	rows, err := s.DB.Query(`
		SELECT b.id, b.name, b.address, b.notes,
		       (SELECT COUNT(*) FROM rooms r WHERE r.building_id = b.id),
		       (SELECT COUNT(*) FROM items i WHERE i.building_id = b.id)
		FROM buildings b ORDER BY b.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Building
	for rows.Next() {
		var b Building
		if err := rows.Scan(&b.ID, &b.Name, &b.Address, &b.Notes, &b.RoomCount, &b.ItemCount); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) GetBuilding(id int64) (*Building, error) {
	var b Building
	err := s.DB.QueryRow(`SELECT id, name, address, notes FROM buildings WHERE id = ?`, id).
		Scan(&b.ID, &b.Name, &b.Address, &b.Notes)
	if err != nil {
		return nil, notFound(err)
	}
	return &b, nil
}

func (s *Store) SaveBuilding(b *Building) error {
	b.Name = strings.TrimSpace(b.Name)
	if b.ID == 0 {
		res, err := s.DB.Exec(`INSERT INTO buildings (name, address, notes) VALUES (?, ?, ?)`, b.Name, b.Address, b.Notes)
		if err != nil {
			return err
		}
		b.ID, err = res.LastInsertId()
		return err
	}
	_, err := s.DB.Exec(`UPDATE buildings SET name = ?, address = ?, notes = ? WHERE id = ?`, b.Name, b.Address, b.Notes, b.ID)
	return err
}

func (s *Store) DeleteBuilding(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM buildings WHERE id = ?`, id)
	return err
}

// Rooms ------------------------------------------------------------------

const roomSelect = `
	SELECT r.id, r.building_id, b.name, r.number, r.name, r.floor, r.notes,
	       (SELECT COUNT(*) FROM items i WHERE i.room_id = r.id)
	FROM rooms r JOIN buildings b ON b.id = r.building_id`

func scanRooms(rows *sql.Rows) ([]Room, error) {
	defer rows.Close()
	var out []Room
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.ID, &r.BuildingID, &r.BuildingName, &r.Number, &r.Name, &r.Floor, &r.Notes, &r.ItemCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRooms returns rooms for one building, or all rooms when buildingID is 0.
func (s *Store) ListRooms(buildingID int64) ([]Room, error) {
	q := roomSelect
	var args []any
	if buildingID != 0 {
		q += ` WHERE r.building_id = ?`
		args = append(args, buildingID)
	}
	q += ` ORDER BY b.name COLLATE NOCASE, b.id, r.floor, r.name COLLATE NOCASE`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	rooms, err := scanRooms(rows)
	if err != nil {
		return nil, err
	}
	// Within each building, numbered rooms come first in natural number
	// order; unnumbered rooms keep the floor/name order from SQL.
	slices.SortStableFunc(rooms, func(a, b Room) int {
		return cmp.Or(
			compareBuildings(a.BuildingName, a.BuildingID, b.BuildingName, b.BuildingID),
			CompareRoomNumbers(a.Number, b.Number),
		)
	})
	return rooms, nil
}

func (s *Store) GetRoom(id int64) (*Room, error) {
	rows, err := s.DB.Query(roomSelect+` WHERE r.id = ?`, id)
	if err != nil {
		return nil, err
	}
	rs, err := scanRooms(rows)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, ErrNotFound
	}
	return &rs[0], nil
}

func (s *Store) SaveRoom(r *Room) error {
	r.Name, r.Number = strings.TrimSpace(r.Name), strings.TrimSpace(r.Number)
	if r.ID == 0 {
		res, err := s.DB.Exec(`INSERT INTO rooms (building_id, number, name, floor, notes) VALUES (?, ?, ?, ?, ?)`, r.BuildingID, r.Number, r.Name, r.Floor, r.Notes)
		if err != nil {
			return err
		}
		r.ID, err = res.LastInsertId()
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE rooms SET building_id = ?, number = ?, name = ?, floor = ?, notes = ? WHERE id = ?`, r.BuildingID, r.Number, r.Name, r.Floor, r.Notes, r.ID); err != nil {
		return err
	}
	// If the room moved to another building, everything in it moves with it.
	for _, table := range []string{"items", "supplies", "problems"} {
		if _, err := tx.Exec(`UPDATE `+table+` SET building_id = ? WHERE room_id = ?`, r.BuildingID, r.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddRooms inserts several new rooms in one transaction: all or nothing.
func (s *Store) AddRooms(rooms []Room) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := range rooms {
		r := &rooms[i]
		r.Name, r.Number = strings.TrimSpace(r.Name), strings.TrimSpace(r.Number)
		res, err := tx.Exec(`INSERT INTO rooms (building_id, number, name, floor, notes) VALUES (?, ?, ?, ?, ?)`, r.BuildingID, r.Number, r.Name, r.Floor, r.Notes)
		if err != nil {
			return err
		}
		if r.ID, err = res.LastInsertId(); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteRoom(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM rooms WHERE id = ?`, id)
	return err
}

// Items ------------------------------------------------------------------

type ItemFilter struct {
	BuildingID int64
	RoomID     int64
	NoRoom     bool // only items not assigned to a room
	SupplyID   int64
	Query      string
}

const itemSelect = `
	SELECT i.id, i.building_id, b.name, COALESCE(i.room_id, 0), COALESCE(r.number, ''), COALESCE(r.name, ''),
	       i.name, i.category, i.manufacturer, i.model, i.serial_number,
	       COALESCE(i.install_date, ''), i.notes, i.quantity, COALESCE(i.supply_id, 0), i.supply_per, i.portable,
	       COALESCE(s.name, ''), COALESCE(s.unit, ''), COALESCE(s.quantity, 0), COALESCE(s.reorder_at, 0),
	       COALESCE(sb.name, ''), COALESCE(sr.number, ''), COALESCE(sr.name, ''),
	       COALESCE((SELECT MIN(t.next_due_on) FROM tasks t WHERE t.item_id = i.id AND t.active = 1), ''),
	       COALESCE((SELECT MAX(l.performed_on) FROM maintenance_logs l WHERE l.item_id = i.id AND l.replaced IS NOT NULL), '')
	FROM items i
	JOIN buildings b ON b.id = i.building_id
	LEFT JOIN rooms r ON r.id = i.room_id
	LEFT JOIN supplies s ON s.id = i.supply_id
	LEFT JOIN buildings sb ON sb.id = s.building_id
	LEFT JOIN rooms sr ON sr.id = s.room_id`

func scanItems(rows *sql.Rows) ([]Item, error) {
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var i Item
		if err := rows.Scan(&i.ID, &i.BuildingID, &i.BuildingName, &i.RoomID, &i.RoomNumber, &i.RoomName,
			&i.Name, &i.Category, &i.Manufacturer, &i.Model, &i.SerialNumber, &i.InstallDate, &i.Notes,
			&i.Quantity, &i.SupplyID, &i.SupplyPer, &i.Portable,
			&i.Supply.Name, &i.Supply.Unit, &i.Supply.Quantity, &i.Supply.ReorderAt,
			&i.Supply.BuildingName, &i.Supply.RoomNumber, &i.Supply.RoomName, &i.NextDue, &i.LastReplaced); err != nil {
			return nil, err
		}
		i.Supply.ID = i.SupplyID
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) ListItems(f ItemFilter) ([]Item, error) {
	q := itemSelect + ` WHERE 1=1`
	var args []any
	if f.BuildingID != 0 {
		q += ` AND i.building_id = ?`
		args = append(args, f.BuildingID)
	}
	if f.RoomID != 0 {
		q += ` AND i.room_id = ?`
		args = append(args, f.RoomID)
	}
	if f.NoRoom {
		q += ` AND i.room_id IS NULL`
	}
	if f.SupplyID != 0 {
		q += ` AND i.supply_id = ?`
		args = append(args, f.SupplyID)
	}
	if f.Query != "" {
		like := "%" + f.Query + "%"
		q += ` AND (i.name LIKE ? OR i.category LIKE ? OR i.manufacturer LIKE ? OR i.model LIKE ? OR i.serial_number LIKE ?)`
		args = append(args, like, like, like, like, like)
	}
	q += ` ORDER BY b.name COLLATE NOCASE, r.name COLLATE NOCASE, i.name COLLATE NOCASE`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return scanItems(rows)
}

func (s *Store) GetItem(id int64) (*Item, error) {
	rows, err := s.DB.Query(itemSelect+` WHERE i.id = ?`, id)
	if err != nil {
		return nil, err
	}
	is, err := scanItems(rows)
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
		newSupply.Name, newSupply.Unit = strings.TrimSpace(newSupply.Name), strings.TrimSpace(newSupply.Unit)
		newSupply.BuildingID, newSupply.Reusable = i.BuildingID, false
		if err := insertSupplyTx(tx, newSupply, userID); err != nil {
			return err
		}
		i.SupplyID = newSupply.ID
	}
	args := []any{i.BuildingID, nullInt(i.RoomID), i.Name, i.Category, i.Manufacturer, i.Model, i.SerialNumber, nullStr(i.InstallDate), i.Notes,
		i.Quantity, nullInt(i.SupplyID), i.SupplyPer, i.Portable}
	if i.ID == 0 {
		res, err := tx.Exec(`INSERT INTO items (building_id, room_id, name, category, manufacturer, model, serial_number, install_date, notes,
			quantity, supply_id, supply_per, portable) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
		if err != nil {
			return err
		}
		if i.ID, err = res.LastInsertId(); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`UPDATE items SET building_id = ?, room_id = ?, name = ?, category = ?, manufacturer = ?, model = ?,
		serial_number = ?, install_date = ?, notes = ?, quantity = ?, supply_id = ?, supply_per = ?, portable = ? WHERE id = ?`, append(args, i.ID)...); err != nil {
		return err
	}
	return tx.Commit()
}

// Move is a request to move an item to another room or building.
type Move struct {
	ItemID     int64
	BuildingID int64
	RoomID     int64 // 0 = building-wide / no room
	MovedOn    string
	Note       string
	UserID     int64
}

// MoveItem changes an item's location and records the move in its
// history. Moving to where it already is records nothing.
func (s *Store) MoveItem(m Move) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fromBuilding, fromRoom int64
	if err := tx.QueryRow(`SELECT building_id, COALESCE(room_id, 0) FROM items WHERE id = ?`, m.ItemID).Scan(&fromBuilding, &fromRoom); err != nil {
		return notFound(err)
	}
	if fromBuilding == m.BuildingID && fromRoom == m.RoomID {
		return nil
	}
	from, err := locationTx(tx, fromBuilding, fromRoom)
	if err != nil {
		return err
	}
	to, err := locationTx(tx, m.BuildingID, m.RoomID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE items SET building_id = ?, room_id = ? WHERE id = ?`, m.BuildingID, nullInt(m.RoomID), m.ItemID); err != nil {
		return err
	}
	notes := "Moved from " + from + " to " + to + "."
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

// locationTx renders "Building › Room" for a building and optional room.
func locationTx(tx *sql.Tx, buildingID, roomID int64) (string, error) {
	var building, number, room string
	if err := tx.QueryRow(`SELECT name FROM buildings WHERE id = ?`, buildingID).Scan(&building); err != nil {
		return "", notFound(err)
	}
	if roomID == 0 {
		return building, nil
	}
	if err := tx.QueryRow(`SELECT number, name FROM rooms WHERE id = ? AND building_id = ?`, roomID, buildingID).Scan(&number, &room); err != nil {
		return "", notFound(err)
	}
	return building + " › " + RoomLabel(number, room), nil
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

// Room numbers -----------------------------------------------------------

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

// compareBuildings matches the SQL "ORDER BY b.name COLLATE NOCASE, b.id".
func compareBuildings(aName string, aID int64, bName string, bID int64) int {
	return cmp.Or(strings.Compare(strings.ToLower(aName), strings.ToLower(bName)), cmp.Compare(aID, bID))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func splitDigits(s string) (digits, rest string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}
