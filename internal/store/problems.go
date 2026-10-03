package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// Problem statuses, in the order they're worked through.
const (
	ProblemOpen       = "open"
	ProblemInProgress = "in_progress"
	ProblemResolved   = "resolved"
)

// ProblemStatuses lists every status in order, for forms.
var ProblemStatuses = []string{ProblemOpen, ProblemInProgress, ProblemResolved}

// ProblemStatusLabel renders a status for people.
func ProblemStatusLabel(status string) string {
	switch status {
	case ProblemOpen:
		return "Open"
	case ProblemInProgress:
		return "In progress"
	case ProblemResolved:
		return "Resolved"
	}
	return status
}

func validProblemStatus(status string) bool {
	return status == ProblemOpen || status == ProblemInProgress || status == ProblemResolved
}

// Problem is something someone reported: a leak, a door that sticks, a
// light out. It belongs to a building and optionally a room and item.
type Problem struct {
	ID              int64
	BuildingID      int64
	BuildingName    string
	RoomID          int64
	RoomNumber      string
	RoomName        string
	ItemID          int64
	ItemName        string
	Title           string
	Details         string
	Status          string
	AssignedTo      int64 // 0 = nobody
	AssignedName    string
	ReportedBy      int64 // 0 when reported without signing in
	ReporterName    string
	ReporterContact string
	CreatedAt       string // UTC "YYYY-MM-DD HH:MM:SS"
	UpdatedAt       string
	ResolvedAt      string
}

func (p Problem) StatusLabel() string { return ProblemStatusLabel(p.Status) }
func (p Problem) Active() bool        { return p.Status != ProblemResolved }

func (p Problem) Location() string {
	if p.RoomName != "" {
		return p.BuildingName + " › " + RoomLabel(p.RoomNumber, p.RoomName)
	}
	return p.BuildingName
}

// ProblemFilter narrows ListProblems. Status is "" for all, "active" for
// open and in progress, or a single status.
type ProblemFilter struct {
	BuildingID int64
	RoomID     int64
	ItemID     int64
	AssignedTo int64
	Status     string
}

const problemSelect = `
	SELECT p.id, p.building_id, b.name, COALESCE(p.room_id, 0), COALESCE(r.number, ''), COALESCE(r.name, ''),
	       COALESCE(p.item_id, 0), COALESCE(i.name, ''), p.title, p.details, p.status,
	       COALESCE(p.assigned_to, 0), COALESCE(NULLIF(a.display_name, ''), a.username, ''),
	       COALESCE(p.reported_by, 0), p.reporter_name, p.reporter_contact,
	       p.created_at, p.updated_at, COALESCE(p.resolved_at, '')
	FROM problems p
	JOIN buildings b ON b.id = p.building_id
	LEFT JOIN rooms r ON r.id = p.room_id
	LEFT JOIN items i ON i.id = p.item_id
	LEFT JOIN users a ON a.id = p.assigned_to`

func scanProblems(rows *sql.Rows) ([]Problem, error) {
	defer rows.Close()
	var out []Problem
	for rows.Next() {
		var p Problem
		if err := rows.Scan(&p.ID, &p.BuildingID, &p.BuildingName, &p.RoomID, &p.RoomNumber, &p.RoomName,
			&p.ItemID, &p.ItemName, &p.Title, &p.Details, &p.Status,
			&p.AssignedTo, &p.AssignedName, &p.ReportedBy, &p.ReporterName, &p.ReporterContact,
			&p.CreatedAt, &p.UpdatedAt, &p.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListProblems returns open problems first, then in progress, then
// resolved; newest first within each.
func (s *Store) ListProblems(f ProblemFilter) ([]Problem, error) {
	q := problemSelect + ` WHERE 1=1`
	var args []any
	if f.BuildingID != 0 {
		q += ` AND p.building_id = ?`
		args = append(args, f.BuildingID)
	}
	if f.RoomID != 0 {
		q += ` AND p.room_id = ?`
		args = append(args, f.RoomID)
	}
	if f.ItemID != 0 {
		q += ` AND p.item_id = ?`
		args = append(args, f.ItemID)
	}
	if f.AssignedTo != 0 {
		q += ` AND p.assigned_to = ?`
		args = append(args, f.AssignedTo)
	}
	switch {
	case f.Status == "active":
		q += ` AND p.status != 'resolved'`
	case f.Status != "":
		q += ` AND p.status = ?`
		args = append(args, f.Status)
	}
	q += ` ORDER BY CASE p.status WHEN 'open' THEN 0 WHEN 'in_progress' THEN 1 ELSE 2 END,
	       COALESCE(p.resolved_at, p.created_at) DESC, p.id DESC`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return scanProblems(rows)
}

func (s *Store) GetProblem(id int64) (*Problem, error) {
	rows, err := s.DB.Query(problemSelect+` WHERE p.id = ?`, id)
	if err != nil {
		return nil, err
	}
	ps, err := scanProblems(rows)
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, ErrNotFound
	}
	return &ps[0], nil
}

// CreateProblem records a new report. It starts open and unassigned.
func (s *Store) CreateProblem(p *Problem) error {
	p.Title, p.Status = strings.TrimSpace(p.Title), ProblemOpen
	res, err := s.DB.Exec(`INSERT INTO problems (building_id, room_id, item_id, title, details, reported_by, reporter_name, reporter_contact)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.BuildingID, nullInt(p.RoomID), nullInt(p.ItemID), p.Title, p.Details, nullInt(p.ReportedBy),
		strings.TrimSpace(p.ReporterName), strings.TrimSpace(p.ReporterContact))
	if err != nil {
		return err
	}
	p.ID, err = res.LastInsertId()
	return err
}

// SaveProblemDetails updates what and where. Status and assignment change
// through UpdateProblem so they show in the timeline.
func (s *Store) SaveProblemDetails(p *Problem) error {
	p.Title = strings.TrimSpace(p.Title)
	_, err := s.DB.Exec(`UPDATE problems SET building_id = ?, room_id = ?, item_id = ?, title = ?, details = ?,
		reporter_name = ?, reporter_contact = ?, updated_at = datetime('now') WHERE id = ?`,
		p.BuildingID, nullInt(p.RoomID), nullInt(p.ItemID), p.Title, p.Details,
		strings.TrimSpace(p.ReporterName), strings.TrimSpace(p.ReporterContact), p.ID)
	return err
}

func (s *Store) DeleteProblem(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM problems WHERE id = ?`, id)
	return err
}

// Problem updates ----------------------------------------------------------

// ProblemChange is a request to move a problem along: a new status, a new
// assignee (0 = nobody), and/or a note.
type ProblemChange struct {
	ProblemID  int64
	Status     string
	AssignedTo int64
	Note       string
	UserID     int64
}

type ProblemUpdate struct {
	ID           int64
	Status       string // new status; "" if unchanged
	Assigned     bool   // whether the assignee changed
	AssignedName string // new assignee; "" when unassigned
	Note         string
	CreatedBy    string
	CreatedAt    string
}

func (u ProblemUpdate) StatusLabel() string { return ProblemStatusLabel(u.Status) }

// UpdateProblem applies a change and adds it to the timeline. It reports
// whether anything changed; a change with nothing new records nothing.
func (s *Store) UpdateProblem(c ProblemChange) (bool, error) {
	if !validProblemStatus(c.Status) {
		return false, fmt.Errorf("unknown problem status %q", c.Status)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var status string
	var assigned int64
	if err := tx.QueryRow(`SELECT status, COALESCE(assigned_to, 0) FROM problems WHERE id = ?`, c.ProblemID).Scan(&status, &assigned); err != nil {
		return false, notFound(err)
	}
	var newStatus, newAssignee any // NULL = unchanged
	if c.Status != status {
		newStatus = c.Status
	}
	if c.AssignedTo != assigned {
		name := ""
		if c.AssignedTo != 0 {
			if err := tx.QueryRow(`SELECT COALESCE(NULLIF(display_name, ''), username) FROM users WHERE id = ?`, c.AssignedTo).Scan(&name); err != nil {
				return false, notFound(err)
			}
		}
		newAssignee = name
	}
	note := strings.TrimSpace(c.Note)
	if newStatus == nil && newAssignee == nil && note == "" {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE problems SET status = ?, assigned_to = ?, updated_at = datetime('now'),
		resolved_at = CASE WHEN ? = 'resolved' THEN COALESCE(resolved_at, datetime('now')) END WHERE id = ?`,
		c.Status, nullInt(c.AssignedTo), c.Status, c.ProblemID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO problem_updates (problem_id, status, assigned_name, note, created_by) VALUES (?, ?, ?, ?, ?)`,
		c.ProblemID, newStatus, newAssignee, note, nullInt(c.UserID)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ListProblemUpdates returns a problem's timeline, oldest first.
func (s *Store) ListProblemUpdates(problemID int64) ([]ProblemUpdate, error) {
	rows, err := s.DB.Query(`
		SELECT pu.id, COALESCE(pu.status, ''), pu.assigned_name IS NOT NULL, COALESCE(pu.assigned_name, ''), pu.note,
		       COALESCE(NULLIF(u.display_name, ''), u.username, ''), pu.created_at
		FROM problem_updates pu
		LEFT JOIN users u ON u.id = pu.created_by
		WHERE pu.problem_id = ?
		ORDER BY pu.id`, problemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProblemUpdate
	for rows.Next() {
		var u ProblemUpdate
		if err := rows.Scan(&u.ID, &u.Status, &u.Assigned, &u.AssignedName, &u.Note, &u.CreatedBy, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
