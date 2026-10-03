package store

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// FrequentReplacements is how many replacements in a year flag a unit as
// worth a closer look (a bad socket, a fixture running hot).
const FrequentReplacements = 3

// Unit is one numbered thing in a group item, e.g. light #7.
type Unit struct {
	Number int
	Label  string // optional location note, e.g. "over the stage, left"
	// From history entries that replaced the item's supply in this unit.
	LastReplaced string
	RecentCount  int // replacements in the past year
}

// Name renders "#7" or "#7 – over the stage, left".
func (u Unit) Name() string { return UnitName(u.Number, u.Label) }

// Frequent reports whether the unit has been replaced unusually often.
func (u Unit) Frequent() bool { return u.RecentCount >= FrequentReplacements }

// UnitName renders "#7" or "#7 – label".
func UnitName(number int, label string) string {
	if label == "" {
		return "#" + strconv.Itoa(number)
	}
	return "#" + strconv.Itoa(number) + " – " + label
}

// UnitsLabel renders "#3, #7".
func UnitsLabel(units []int) string {
	parts := make([]string, len(units))
	for i, u := range units {
		parts[i] = "#" + strconv.Itoa(u)
	}
	return strings.Join(parts, ", ")
}

// ItemUnits returns units #1..#quantity of an item with their notes and
// replacement history. today bounds the "past year" count.
func (s *Store) ItemUnits(itemID int64, quantity int, today time.Time) ([]Unit, error) {
	units := make([]Unit, quantity)
	for i := range units {
		units[i].Number = i + 1
	}
	rows, err := s.DB.Query(`SELECT number, label FROM item_units WHERE item_id = ? AND number <= ?`, itemID, quantity)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n int
		var label string
		if err := rows.Scan(&n, &label); err != nil {
			rows.Close()
			return nil, err
		}
		units[n-1].Label = label
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

// SaveUnitLabels replaces an item's unit notes; blank notes are dropped.
func (s *Store) SaveUnitLabels(itemID int64, labels map[int]string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM item_units WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for n, label := range labels {
		if label = strings.TrimSpace(label); label == "" || n < 1 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO item_units (item_id, number, label) VALUES (?, ?, ?)`, itemID, n, label); err != nil {
			return err
		}
	}
	return tx.Commit()
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
