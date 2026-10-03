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
	// Earliest due date across the item's active tasks ("" if none).
	NextDue string
}

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
	// If the room moved to another building, its items and supplies move with it.
	if _, err := tx.Exec(`UPDATE items SET building_id = ? WHERE room_id = ?`, r.BuildingID, r.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE supplies SET building_id = ? WHERE room_id = ?`, r.BuildingID, r.ID); err != nil {
		return err
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
	Query      string
}

const itemSelect = `
	SELECT i.id, i.building_id, b.name, COALESCE(i.room_id, 0), COALESCE(r.number, ''), COALESCE(r.name, ''),
	       i.name, i.category, i.manufacturer, i.model, i.serial_number,
	       COALESCE(i.install_date, ''), i.notes,
	       COALESCE((SELECT MIN(t.next_due_on) FROM tasks t WHERE t.item_id = i.id AND t.active = 1), '')
	FROM items i
	JOIN buildings b ON b.id = i.building_id
	LEFT JOIN rooms r ON r.id = i.room_id`

func scanItems(rows *sql.Rows) ([]Item, error) {
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var i Item
		if err := rows.Scan(&i.ID, &i.BuildingID, &i.BuildingName, &i.RoomID, &i.RoomNumber, &i.RoomName,
			&i.Name, &i.Category, &i.Manufacturer, &i.Model, &i.SerialNumber, &i.InstallDate, &i.Notes, &i.NextDue); err != nil {
			return nil, err
		}
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

func (s *Store) SaveItem(i *Item) error {
	i.Name = strings.TrimSpace(i.Name)
	args := []any{i.BuildingID, nullInt(i.RoomID), i.Name, strings.TrimSpace(i.Category), i.Manufacturer, i.Model, i.SerialNumber, nullStr(i.InstallDate), i.Notes}
	if i.ID == 0 {
		res, err := s.DB.Exec(`INSERT INTO items (building_id, room_id, name, category, manufacturer, model, serial_number, install_date, notes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
		if err != nil {
			return err
		}
		i.ID, err = res.LastInsertId()
		return err
	}
	_, err := s.DB.Exec(`UPDATE items SET building_id = ?, room_id = ?, name = ?, category = ?, manufacturer = ?, model = ?,
		serial_number = ?, install_date = ?, notes = ? WHERE id = ?`, append(args, i.ID)...)
	return err
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
