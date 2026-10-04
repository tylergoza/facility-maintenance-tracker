package store

import (
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// FrequentReplacements is how many replacements in a year flag a unit as
// worth a closer look (a bad socket, a fixture running hot).
const FrequentReplacements = 3

// Unit is one numbered thing in an item, e.g. light #7. Its number is
// its place in the item's list and changes when others move out; its tag
// is its ID, what's on its sticker, and goes wherever it does.
//
// IDs start out as the unit's number and are unique among its kind: the
// units of every portable item with the same name ("Lapel mics" 1, 2, 3
// across rooms), or of just its own item when it isn't portable.
type Unit struct {
	Number int
	Tag    string // its ID, e.g. "2" or "Mic 2"
	Label  string // optional location note, e.g. "over the stage, left"
	// From history entries that replaced the item's supply in this unit.
	LastReplaced string
	RecentCount  int // replacements in the past year
}

// Name renders "#7", "#1 (ID 2)" or "#7 – over the stage, left".
func (u Unit) Name() string { return UnitName(u.Number, u.Tag, u.Label) }

// ID is what it's known by: its tag, or its number if it has none yet.
func (u Unit) ID() string { return unitID(u.Number, u.Tag) }

// Tagged reports whether its ID differs from its number.
func (u Unit) Tagged() bool { return u.ID() != strconv.Itoa(u.Number) }

// Frequent reports whether the unit has been replaced unusually often.
func (u Unit) Frequent() bool { return u.RecentCount >= FrequentReplacements }

// UnitName renders "#7", "#1 (ID 2)" or "#7 – label".
func UnitName(number int, tag, label string) string {
	name := "#" + strconv.Itoa(number)
	if id := unitID(number, tag); id != strconv.Itoa(number) {
		name += " (ID " + id + ")"
	}
	if label != "" {
		name += " – " + label
	}
	return name
}

func unitID(number int, tag string) string {
	if tag = strings.TrimSpace(tag); tag != "" {
		return tag
	}
	return strconv.Itoa(number)
}

// UnitsLabel renders "#3, #7".
func UnitsLabel(units []int) string {
	parts := make([]string, len(units))
	for i, u := range units {
		parts[i] = "#" + strconv.Itoa(u)
	}
	return strings.Join(parts, ", ")
}

// unitRange renders "#1–#7", or "#3" for a single unit.
func unitRange(first, last int) string {
	if first == last {
		return "#" + strconv.Itoa(first)
	}
	return "#" + strconv.Itoa(first) + "–#" + strconv.Itoa(last)
}

// ItemUnits returns units #1..#quantity of an item with their IDs, notes and
// replacement history. today bounds the "past year" count.
func (s *Store) ItemUnits(itemID int64, quantity int, today time.Time) ([]Unit, error) {
	units := make([]Unit, quantity)
	for i := range units {
		units[i].Number = i + 1
	}
	rows, err := s.DB.Query(`SELECT number, tag, label FROM item_units WHERE item_id = ? AND number <= ?`, itemID, quantity)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n int
		var tag, label string
		if err := rows.Scan(&n, &tag, &label); err != nil {
			rows.Close()
			return nil, err
		}
		units[n-1].Tag, units[n-1].Label = tag, label
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	yearAgo := today.AddDate(-1, 0, 0).Format(DateLayout)
	rows, err = s.DB.Query(`
		SELECT u.unit, MAX(l.performed_on), SUM(l.performed_on > ?)
		FROM log_units u JOIN maintenance_logs l ON l.id = u.log_id
		WHERE l.item_id = ? AND l.replaced IS NOT NULL AND u.unit <= ?
		GROUP BY u.unit`, yearAgo, itemID, quantity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n, recent int
		var last string
		if err := rows.Scan(&n, &last, &recent); err != nil {
			return nil, err
		}
		units[n-1].LastReplaced, units[n-1].RecentCount = last, recent
	}
	return units, rows.Err()
}

// SaveUnits replaces an item's unit IDs and notes. Units without an ID
// are given one.
func (s *Store) SaveUnits(itemID int64, units []Unit) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM item_units WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for _, u := range units {
		if err := saveUnit(tx, itemID, u); err != nil {
			return err
		}
	}
	if err := fillUnitIDs(tx, itemID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetUnitIDs gives units #1..#quantity of an item the IDs listed, in
// order, keeping their notes. Units past the end of the list, and blank
// entries, are given one.
func (s *Store) SetUnitIDs(itemID int64, quantity int, ids []string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	labels := map[int]string{}
	rows, err := tx.Query(`SELECT number, label FROM item_units WHERE item_id = ?`, itemID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n int
		var label string
		if err := rows.Scan(&n, &label); err != nil {
			rows.Close()
			return err
		}
		labels[n] = label
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM item_units WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for n := 1; n <= quantity; n++ {
		u := Unit{Number: n, Label: labels[n]}
		if n <= len(ids) {
			u.Tag = ids[n-1]
		}
		if err := saveUnit(tx, itemID, u); err != nil {
			return err
		}
	}
	if err := fillUnitIDs(tx, itemID); err != nil {
		return err
	}
	return tx.Commit()
}

// saveUnit writes one unit's ID and note, if it has either.
func saveUnit(tx *sql.Tx, itemID int64, u Unit) error {
	u.Tag, u.Label = strings.TrimSpace(u.Tag), strings.TrimSpace(u.Label)
	if u.Number < 1 || (u.Tag == "" && u.Label == "") {
		return nil
	}
	_, err := tx.Exec(`INSERT OR REPLACE INTO item_units (item_id, number, tag, label) VALUES (?, ?, ?, ?)`, itemID, u.Number, u.Tag, u.Label)
	return err
}

// UnitIDsTaken returns the IDs, lower-cased, that other items of the same
// kind use: other portable items named name, when portable. itemID is the
// item asking (0 for a new one).
func (s *Store) UnitIDsTaken(itemID int64, name string, portable bool) (map[string]bool, error) {
	return unitIDsTaken(s.DB, itemID, name, portable)
}

func unitIDsTaken(q querier, itemID int64, name string, portable bool) (map[string]bool, error) {
	taken := map[string]bool{}
	if !portable {
		return taken, nil
	}
	rows, err := q.Query(`
		SELECT u.tag FROM items o JOIN item_units u ON u.item_id = o.id AND u.number <= o.quantity
		WHERE o.portable = 1 AND o.name = ? COLLATE NOCASE AND o.id != ? AND u.tag != ''`, strings.TrimSpace(name), itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		taken[strings.ToLower(tag)] = true
	}
	return taken, rows.Err()
}

// fillUnitIDs drops an item's units past its count and gives each unit
// without an ID one: its number when no other of its kind has that, or
// else the number after the highest in use.
func fillUnitIDs(tx *sql.Tx, itemID int64) error {
	var name string
	var quantity int
	var portable bool
	if err := tx.QueryRow(`SELECT name, quantity, portable FROM items WHERE id = ?`, itemID).Scan(&name, &quantity, &portable); err != nil {
		return notFound(err)
	}
	if _, err := tx.Exec(`DELETE FROM item_units WHERE item_id = ? AND number > ?`, itemID, quantity); err != nil {
		return err
	}
	taken, err := unitIDsTaken(tx, itemID, name, portable)
	if err != nil {
		return err
	}
	tags := map[int]string{}
	rows, err := tx.Query(`SELECT number, tag FROM item_units WHERE item_id = ? AND tag != ''`, itemID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n int
		var tag string
		if err := rows.Scan(&n, &tag); err != nil {
			rows.Close()
			return err
		}
		tags[n] = tag
		taken[strings.ToLower(tag)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	next := 1
	for tag := range taken {
		if v, err := strconv.Atoi(tag); err == nil && v >= next {
			next = v + 1
		}
	}
	for n := 1; n <= quantity; n++ {
		if tags[n] != "" {
			continue
		}
		id := strconv.Itoa(n)
		if taken[id] {
			for taken[strconv.Itoa(next)] {
				next++
			}
			id = strconv.Itoa(next)
		}
		taken[id] = true
		if _, err := tx.Exec(`INSERT INTO item_units (item_id, number, tag) VALUES (?, ?, ?)
			ON CONFLICT (item_id, number) DO UPDATE SET tag = excluded.tag`, itemID, n, id); err != nil {
			return err
		}
	}
	return nil
}

// parseUnits turns SQLite's group_concat output ("3,7") into numbers.
func parseUnits(s string) []int {
	if s == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(part); err == nil {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// checkUnits reports an error if any unit is outside 1..quantity.
func checkUnits(units []int, quantity int) error {
	for _, u := range units {
		if u < 1 || u > quantity {
			return fmt.Errorf("unit #%d is not between 1 and %d", u, quantity)
		}
	}
	return nil
}
