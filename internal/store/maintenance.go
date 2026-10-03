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
	BuildingID      int64
	BuildingName    string
	RoomID          int64
	RoomNumber      string
	RoomName        string
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

func (t Task) Location() string {
	if t.RoomName != "" {
		return t.BuildingName + " › " + RoomLabel(t.RoomNumber, t.RoomName)
	}
	return t.BuildingName
}

type TaskFilter struct {
	ItemID     int64
	BuildingID int64
	SupplyID   int64
	ActiveOnly bool
}

const taskSelect = `
	SELECT t.id, t.item_id, i.name, i.category, i.building_id, b.name, COALESCE(i.room_id, 0), COALESCE(r.number, ''), COALESCE(r.name, ''),
	       t.name, t.description, COALESCE(t.interval_value, 0), COALESCE(t.interval_unit, ''),
	       COALESCE(t.last_completed_on, ''), COALESCE(t.next_due_on, ''), t.active,
	       COALESCE(t.supply_id, 0), t.supply_amount, t.supply_always, COALESCE(s.name, ''), COALESCE(s.unit, ''),
	       COALESCE(s.quantity, 0), COALESCE(s.reorder_at, 0), COALESCE(sb.name, ''), COALESCE(sr.number, ''), COALESCE(sr.name, '')
	FROM tasks t
	JOIN items i ON i.id = t.item_id
	JOIN buildings b ON b.id = i.building_id
	LEFT JOIN rooms r ON r.id = i.room_id
	LEFT JOIN supplies s ON s.id = t.supply_id
	LEFT JOIN buildings sb ON sb.id = s.building_id
	LEFT JOIN rooms sr ON sr.id = s.room_id`

func scanTasks(rows *sql.Rows) ([]Task, error) {
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.ItemID, &t.ItemName, &t.ItemCategory, &t.BuildingID, &t.BuildingName, &t.RoomID, &t.RoomNumber, &t.RoomName,
			&t.Name, &t.Description, &t.IntervalValue, &t.IntervalUnit, &t.LastCompletedOn, &t.NextDueOn, &t.Active,
			&t.SupplyID, &t.SupplyAmount, &t.SupplyAlways, &t.Supply.Name, &t.Supply.Unit, &t.Supply.Quantity, &t.Supply.ReorderAt,
			&t.Supply.BuildingName, &t.Supply.RoomNumber, &t.Supply.RoomName); err != nil {
			return nil, err
		}
		t.Supply.ID = t.SupplyID
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTasks returns tasks ordered by due date, unscheduled last.
func (s *Store) ListTasks(f TaskFilter) ([]Task, error) {
	q := taskSelect + ` WHERE 1=1`
	var args []any
	if f.ItemID != 0 {
		q += ` AND t.item_id = ?`
		args = append(args, f.ItemID)
	}
	if f.BuildingID != 0 {
		q += ` AND i.building_id = ?`
		args = append(args, f.BuildingID)
	}
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
	return scanTasks(rows)
}

func (s *Store) GetTask(id int64) (*Task, error) {
	rows, err := s.DB.Query(taskSelect+` WHERE t.id = ?`, id)
	if err != nil {
		return nil, err
	}
	ts, err := scanTasks(rows)
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
	ID           int64
	ItemID       int64
	ItemName     string
	TaskID       int64
	TaskName     string
	BuildingName string
	RoomNumber   string
	RoomName     string
	PerformedOn  string
	PerformedBy  string
	CostCents    int64
	Notes        string
	CreatedBy    string
	SupplyUsed   string // e.g. "1 × Furnace filters"; blank if none
}

func (l Log) Location() string {
	if l.RoomName != "" {
		return l.BuildingName + " › " + RoomLabel(l.RoomNumber, l.RoomName)
	}
	return l.BuildingName
}

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
		SELECT l.id, l.item_id, i.name, COALESCE(l.task_id, 0), COALESCE(t.name, ''), b.name, COALESCE(r.number, ''), COALESCE(r.name, ''),
		       l.performed_on, l.performed_by, COALESCE(l.cost_cents, 0), l.notes,
		       COALESCE(NULLIF(u.display_name, ''), u.username, ''),
		       COALESCE((SELECT c.amount || ' × ' || s.name FROM supply_changes c JOIN supplies s ON s.id = c.supply_id
		                 WHERE c.log_id = l.id AND c.kind = 'used' LIMIT 1), '')
		FROM maintenance_logs l
		JOIN items i ON i.id = l.item_id
		JOIN buildings b ON b.id = i.building_id
		LEFT JOIN rooms r ON r.id = i.room_id
		LEFT JOIN tasks t ON t.id = l.task_id
		LEFT JOIN users u ON u.id = l.created_by
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
		if err := rows.Scan(&l.ID, &l.ItemID, &l.ItemName, &l.TaskID, &l.TaskName, &l.BuildingName, &l.RoomNumber, &l.RoomName,
			&l.PerformedOn, &l.PerformedBy, &l.CostCents, &l.Notes, &l.CreatedBy, &l.SupplyUsed); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
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
	rows, err := tx.Query(`SELECT supply_id, -delta FROM supply_changes WHERE log_id = ? AND kind = 'used'`, id)
	if err != nil {
		return 0, err
	}
	var giveBack []Adjustment
	for rows.Next() {
		a := Adjustment{Kind: "restocked", Note: "Put back: history entry deleted", UserID: userID}
		if err := rows.Scan(&a.SupplyID, &a.Amount); err != nil {
			rows.Close()
			return 0, err
		}
		giveBack = append(giveBack, a)
	}
	rows.Close()
	for _, a := range giveBack {
		if err := adjustSupplyTx(tx, a); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM maintenance_logs WHERE id = ?`, id); err != nil {
		return 0, err
	}
	return itemID, tx.Commit()
}

// Completion records maintenance that was performed.
type Completion struct {
	ItemID      int64
	TaskID      int64 // 0 for ad-hoc maintenance not tied to a task
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

	res, err := tx.Exec(`INSERT INTO maintenance_logs (item_id, task_id, performed_on, performed_by, cost_cents, notes, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, c.ItemID, nullInt(c.TaskID), c.PerformedOn, c.PerformedBy, nullInt(c.CostCents), c.Notes, nullInt(c.UserID))
	if err != nil {
		return err
	}
	if c.SupplyID != 0 && c.SupplyAmount > 0 {
		logID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		var itemName, taskName string // only for the history note; blank is fine
		_ = tx.QueryRow(`SELECT i.name, COALESCE(t.name, '') FROM items i LEFT JOIN tasks t ON t.id = ? WHERE i.id = ?`, c.TaskID, c.ItemID).
			Scan(&itemName, &taskName)
		note := "For " + itemName
		if taskName != "" {
			note = taskName + " (" + itemName + ")"
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
	Buildings int
	Rooms     int
	Items     int
	Tasks     int
}

func (s *Store) Counts() (Counts, error) {
	var c Counts
	err := s.DB.QueryRow(`SELECT
		(SELECT COUNT(*) FROM buildings), (SELECT COUNT(*) FROM rooms),
		(SELECT COUNT(*) FROM items), (SELECT COUNT(*) FROM tasks WHERE active = 1)`).
		Scan(&c.Buildings, &c.Rooms, &c.Items, &c.Tasks)
	return c, err
}
