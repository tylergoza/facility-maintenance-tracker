package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const DateLayout = "2006-01-02"

// Status describes where a task sits relative to its due date.
type Status string

const (
	StatusOverdue     Status = "overdue"
	StatusDueSoon     Status = "due-soon"
	StatusUpcoming    Status = "upcoming"
	StatusUnscheduled Status = "unscheduled"
	StatusInactive    Status = "inactive"
)

func (s Status) Label() string {
	switch s {
	case StatusOverdue:
		return "Overdue"
	case StatusDueSoon:
		return "Due soon"
	case StatusUpcoming:
		return "Upcoming"
	case StatusUnscheduled:
		return "Not scheduled"
	case StatusInactive:
		return "Completed"
	}
	return string(s)
}

// Rank orders statuses from most to least urgent.
func (s Status) Rank() int {
	switch s {
	case StatusOverdue:
		return 0
	case StatusDueSoon:
		return 1
	case StatusUpcoming:
		return 2
	case StatusUnscheduled:
		return 3
	}
	return 4
}

// StatusFor classifies a due date (YYYY-MM-DD, or "" if unscheduled).
func StatusFor(nextDue string, today time.Time, dueSoonDays int) Status {
	if nextDue == "" {
		return StatusUnscheduled
	}
	due, err := time.Parse(DateLayout, nextDue)
	if err != nil {
		return StatusUnscheduled
	}
	today = truncateDay(today)
	switch {
	case due.Before(today):
		return StatusOverdue
	case !due.After(today.AddDate(0, 0, dueSoonDays)):
		return StatusDueSoon
	default:
		return StatusUpcoming
	}
}

func truncateDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// DaysUntil returns whole days from today to date (negative if past).
func DaysUntil(date string, today time.Time) (int, bool) {
	d, err := time.Parse(DateLayout, date)
	if err != nil {
		return 0, false
	}
	return int(d.Sub(truncateDay(today)).Hours() / 24), true
}

// AddInterval advances a YYYY-MM-DD date by the given interval.
func AddInterval(date string, value int, unit string) (string, error) {
	d, err := time.Parse(DateLayout, date)
	if err != nil {
		return "", err
	}
	switch unit {
	case "days":
		d = d.AddDate(0, 0, value)
	case "weeks":
		d = d.AddDate(0, 0, 7*value)
	case "months":
		d = addMonthsClamped(d, value)
	case "years":
		d = addMonthsClamped(d, 12*value)
	default:
		return "", fmt.Errorf("unknown interval unit %q", unit)
	}
	return d.Format(DateLayout), nil
}

// addMonthsClamped adds months without Go's day overflow, so Jan 31 + 1
// month is Feb 28/29 rather than early March.
func addMonthsClamped(d time.Time, months int) time.Time {
	first := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, months, 0)
	lastDay := first.AddDate(0, 1, -1).Day()
	day := d.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}

// Tasks ------------------------------------------------------------------

type Task struct {
	ID              int64
	ItemID          int64
	ItemName        string
	ItemCategory    string
	PlaceID         int64 // where the item is
	Place           Place
	Name            string
	Description     string
	IntervalValue   int
	IntervalUnit    string
	LastCompletedOn string
	NextDueOn       string
	Active          bool
	// Optional supply the task uses (SupplyID 0 = none). SupplyAlways is
	// true when it's used up every time ("Replace filter") and false when
	// only sometimes ("Check filter"). Supply holds its current stock.
	SupplyID     int64
	SupplyAmount int
	SupplyAlways bool
	Supply       Supply
}

// SupplyShort reports whether there's not enough of the task's supply on
// hand to do it once.
func (t Task) SupplyShort() bool { return t.SupplyID != 0 && t.Supply.Quantity < t.SupplyAmount }

func (t Task) Recurring() bool { return t.IntervalValue > 0 && t.IntervalUnit != "" }

func (t Task) IntervalLabel() string {
	if !t.Recurring() {
		return "One time"
	}
	unit := strings.TrimSuffix(t.IntervalUnit, "s")
	if t.IntervalValue == 1 {
		return "Every " + unit
	}
	return fmt.Sprintf("Every %d %ss", t.IntervalValue, unit)
}

// Location renders where the item is.
func (t Task) Location() string { return t.Place.Path }

type TaskFilter struct {
	ItemID     int64
	PlaceID    int64 // items in this place or anywhere inside it
	SupplyID   int64
	ActiveOnly bool
}

const taskSelect = `
	SELECT t.id, t.item_id, i.name, i.category, i.place_id,
	       t.name, t.description, COALESCE(t.interval_value, 0), COALESCE(t.interval_unit, ''),
	       COALESCE(t.last_completed_on, ''), COALESCE(t.next_due_on, ''), t.active,
	       COALESCE(t.supply_id, 0), t.supply_amount, t.supply_always, COALESCE(s.name, ''), COALESCE(s.unit, ''),
	       COALESCE(s.quantity, 0), COALESCE(s.reorder_at, 0), COALESCE(s.place_id, 0)
	FROM tasks t
	JOIN items i ON i.id = t.item_id
	LEFT JOIN supplies s ON s.id = t.supply_id`

// scanTasks reads tasks and fills in where their items and supplies are.
func (s *Store) scanTasks(rows *sql.Rows) ([]Task, error) {
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.ItemID, &t.ItemName, &t.ItemCategory, &t.PlaceID,
			&t.Name, &t.Description, &t.IntervalValue, &t.IntervalUnit, &t.LastCompletedOn, &t.NextDueOn, &t.Active,
			&t.SupplyID, &t.SupplyAmount, &t.SupplyAlways, &t.Supply.Name, &t.Supply.Unit, &t.Supply.Quantity, &t.Supply.ReorderAt,
			&t.Supply.PlaceID); err != nil {
			return nil, err
		}
		t.Supply.ID = t.SupplyID
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	tree, err := loadPlaces(s.DB)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Place, _ = tree.Get(out[i].PlaceID)
		out[i].Supply.Place, _ = tree.Get(out[i].Supply.PlaceID)
	}
	return out, nil
}

// ListTasks returns tasks ordered by due date, unscheduled last.
func (s *Store) ListTasks(f TaskFilter) ([]Task, error) {
	q := taskSelect + ` WHERE 1=1`
	var args []any
	if f.ItemID != 0 {
		q += ` AND t.item_id = ?`
		args = append(args, f.ItemID)
	}
	q, args = placeFilter(q, args, "i.place_id", f.PlaceID, false, "")
	if f.SupplyID != 0 {
		q += ` AND t.supply_id = ?`
		args = append(args, f.SupplyID)
	}
	if f.ActiveOnly {
		q += ` AND t.active = 1`
	}
	q += ` ORDER BY t.active DESC, t.next_due_on IS NULL, t.next_due_on, t.name COLLATE NOCASE`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return s.scanTasks(rows)
}

func (s *Store) GetTask(id int64) (*Task, error) {
	rows, err := s.DB.Query(taskSelect+` WHERE t.id = ?`, id)
	if err != nil {
		return nil, err
	}
	ts, err := s.scanTasks(rows)
	if err != nil {
		return nil, err
	}
	if len(ts) == 0 {
		return nil, ErrNotFound
	}
	return &ts[0], nil
}

func (s *Store) SaveTask(t *Task) error {
	t.Name = strings.TrimSpace(t.Name)
	var ivalue, iunit any
	if t.Recurring() {
		ivalue, iunit = t.IntervalValue, t.IntervalUnit
	}
	if t.SupplyAmount < 1 {
		t.SupplyAmount = 1
	}
	args := []any{t.ItemID, t.Name, t.Description, ivalue, iunit, nullStr(t.LastCompletedOn), nullStr(t.NextDueOn), t.Active,
		nullInt(t.SupplyID), t.SupplyAmount, t.SupplyAlways}
	if t.ID == 0 {
		res, err := s.DB.Exec(`INSERT INTO tasks (item_id, name, description, interval_value, interval_unit, last_completed_on, next_due_on, active,
			supply_id, supply_amount, supply_always) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
		if err != nil {
			return err
		}
		t.ID, err = res.LastInsertId()
		return err
	}
	_, err := s.DB.Exec(`UPDATE tasks SET item_id = ?, name = ?, description = ?, interval_value = ?, interval_unit = ?,
		last_completed_on = ?, next_due_on = ?, active = ?, supply_id = ?, supply_amount = ?, supply_always = ? WHERE id = ?`, append(args, t.ID)...)
	return err
}

func (s *Store) DeleteTask(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM tasks WHERE id = ?`, id)
	return err
}

// Logs -------------------------------------------------------------------

type Log struct {
	ID          int64
	ItemID      int64
	ItemName    string
	TaskID      int64
	TaskName    string
	Kind        string // "work", "replaced" or "moved"
	Replaced    int    // how many of the item's supply were replaced; 0 if none
	SupplyName  string // the item's supply, for "Replaced 2 × …"
	Units       []int  // which of a group item's units it was about
	Place       Place  // where the item is now
	PerformedOn string
	PerformedBy string
	CostCents   int64
	Notes       string
	CreatedBy   string
	SupplyUsed  string // e.g. "1 × Furnace filters"; blank if none
}

// UnitsLabel renders "#3, #7", or "" when no units were picked.
func (l Log) UnitsLabel() string { return UnitsLabel(l.Units) }

// Location renders where the item is now.
func (l Log) Location() string { return l.Place.Path }

func (l Log) Cost() string {
	if l.CostCents == 0 {
		return ""
	}
	return fmt.Sprintf("$%d.%02d", l.CostCents/100, l.CostCents%100)
}

type LogFilter struct {
	ItemID int64
	Limit  int
}

func (s *Store) ListLogs(f LogFilter) ([]Log, error) {
	q := `
		SELECT l.id, l.item_id, i.name, COALESCE(l.task_id, 0), COALESCE(t.name, ''), l.kind, COALESCE(l.replaced, 0), COALESCE(s.name, ''),
		       i.place_id,
		       l.performed_on, l.performed_by, COALESCE(l.cost_cents, 0), l.notes,
		       COALESCE(NULLIF(u.display_name, ''), u.username, ''),
		       COALESCE((SELECT c.amount || ' × ' || s.name FROM supply_changes c JOIN supplies s ON s.id = c.supply_id
		                 WHERE c.log_id = l.id AND c.kind = 'used' LIMIT 1), ''),
		       COALESCE((SELECT group_concat(unit) FROM log_units WHERE log_id = l.id), '')
		FROM maintenance_logs l
		JOIN items i ON i.id = l.item_id
		LEFT JOIN tasks t ON t.id = l.task_id
		LEFT JOIN users u ON u.id = l.created_by
		LEFT JOIN supplies s ON s.id = i.supply_id
		WHERE 1=1`
	var args []any
	if f.ItemID != 0 {
		q += ` AND l.item_id = ?`
		args = append(args, f.ItemID)
	}
	q += ` ORDER BY l.performed_on DESC, l.id DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Log
	for rows.Next() {
		var l Log
		var units string
		if err := rows.Scan(&l.ID, &l.ItemID, &l.ItemName, &l.TaskID, &l.TaskName, &l.Kind, &l.Replaced, &l.SupplyName,
			&l.Place.ID,
			&l.PerformedOn, &l.PerformedBy, &l.CostCents, &l.Notes, &l.CreatedBy, &l.SupplyUsed, &units); err != nil {
			return nil, err
		}
		l.Units = parseUnits(units)
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	tree, err := loadPlaces(s.DB)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Place, _ = tree.Get(out[i].Place.ID)
	}
	return out, nil
}

// DeleteLog removes a history entry. Any supply taken as part of it is put
// back, so a mistaken entry doesn't leave the count short.
func (s *Store) DeleteLog(id int64, userID int64) (itemID int64, err error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`SELECT item_id FROM maintenance_logs WHERE id = ?`, id).Scan(&itemID); err != nil {
		return 0, notFound(err)
	}
	if err := putBackTx(tx, id, userID, "Put back: history entry deleted"); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM maintenance_logs WHERE id = ?`, id); err != nil {
		return 0, err
	}
	return itemID, tx.Commit()
}

// Completion records maintenance that was performed.
type Completion struct {
	ItemID int64
	TaskID int64  // 0 for ad-hoc maintenance not tied to a task
	Kind   string // "work" (default) or "replaced"
	// Replaced is how many of the item's own supply were replaced. Taking
	// the item's supply from stock counts as replacing it.
	Replaced int
	// Units are which of a group item's numbered units the work was on.
	Units       []int
	PerformedOn string
	PerformedBy string
	CostCents   int64
	Notes       string
	UserID      int64
	// NextDueOn overrides the computed next due date when set.
	NextDueOn string
	// SupplyID/SupplyAmount, when set, take that much of a supply out of
	// stock as part of the same record.
	SupplyID     int64
	SupplyAmount int
}

// RecordMaintenance logs work and, when tied to a task, rolls the task
// forward: recurring tasks get a new due date, one-off tasks are closed.
func (s *Store) RecordMaintenance(c Completion) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if c.Kind == "" {
		c.Kind = "work"
	}
	var itemSupply int64
	var quantity int
	if err := tx.QueryRow(`SELECT COALESCE(supply_id, 0), quantity FROM items WHERE id = ?`, c.ItemID).Scan(&itemSupply, &quantity); err != nil {
		return notFound(err)
	}
	if err := checkUnits(c.Units, quantity); err != nil {
		return err
	}
	if c.Replaced == 0 && c.SupplyID != 0 && c.SupplyID == itemSupply {
		c.Replaced = c.SupplyAmount
	}
	res, err := tx.Exec(`INSERT INTO maintenance_logs (item_id, task_id, kind, replaced, performed_on, performed_by, cost_cents, notes, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.ItemID, nullInt(c.TaskID), c.Kind, nullInt(int64(c.Replaced)),
		c.PerformedOn, c.PerformedBy, nullInt(c.CostCents), c.Notes, nullInt(c.UserID))
	if err != nil {
		return err
	}
	logID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, u := range c.Units {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO log_units (log_id, unit) VALUES (?, ?)`, logID, u); err != nil {
			return err
		}
	}
	if c.SupplyID != 0 && c.SupplyAmount > 0 {
		var itemName, taskName, number, place string // only for the history note; blank is fine
		_ = tx.QueryRow(`SELECT i.name, COALESCE(t.name, ''), p.number, p.name
			FROM items i JOIN places p ON p.id = i.place_id LEFT JOIN tasks t ON t.id = ? WHERE i.id = ?`, c.TaskID, c.ItemID).
			Scan(&itemName, &taskName, &number, &place)
		if place != "" {
			itemName += ", " + RoomLabel(number, place)
		}
		if len(c.Units) > 0 {
			itemName += ", " + UnitsLabel(c.Units)
		}
		note := "For " + itemName
		switch {
		case taskName != "":
			note = taskName + " (" + itemName + ")"
		case c.Kind == "replaced":
			note = "Replaced in " + itemName
		}
		if err := adjustSupplyTx(tx, Adjustment{SupplyID: c.SupplyID, Kind: "used", Amount: c.SupplyAmount, Note: note, UserID: c.UserID, LogID: logID}); err != nil {
			return err
		}
	}

	if c.TaskID != 0 {
		var ivalue int
		var iunit, last string
		if err := tx.QueryRow(`SELECT COALESCE(interval_value, 0), COALESCE(interval_unit, ''), COALESCE(last_completed_on, '') FROM tasks WHERE id = ?`, c.TaskID).
			Scan(&ivalue, &iunit, &last); err != nil {
			return notFound(err)
		}
		// Back-dated entries must not move "last completed" backwards.
		if c.PerformedOn > last {
			last = c.PerformedOn
		}
		next, active := c.NextDueOn, true
		if next == "" {
			if ivalue > 0 && iunit != "" {
				if next, err = AddInterval(last, ivalue, iunit); err != nil {
					return err
				}
			} else {
				active = false
			}
		}
		if _, err := tx.Exec(`UPDATE tasks SET last_completed_on = ?, next_due_on = ?, active = ? WHERE id = ?`,
			last, nullStr(next), active, c.TaskID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Summary ----------------------------------------------------------------

type Counts struct {
	Places int
	Items  int
	Tasks  int
}

func (s *Store) Counts() (Counts, error) {
	var c Counts
	err := s.DB.QueryRow(`SELECT
		(SELECT COUNT(*) FROM places), (SELECT COUNT(*) FROM items), (SELECT COUNT(*) FROM tasks WHERE active = 1)`).
		Scan(&c.Places, &c.Items, &c.Tasks)
	return c, err
}
