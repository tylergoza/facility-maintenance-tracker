package store

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrNotEnough is returned when an adjustment would take more out of a
// bucket (on hand, in use, out for cleaning) than it holds. The concrete
// error is a *NotEnoughError.
var ErrNotEnough = errors.New("not enough on hand")

type NotEnoughError struct {
	Bucket string // "on hand", "in use" or "out for cleaning"
	Have   int
}

func (e *NotEnoughError) Error() string { return fmt.Sprintf("only %d %s", e.Have, e.Bucket) }
func (e *NotEnoughError) Unwrap() error { return ErrNotEnough }

type Supply struct {
	ID        int64
	PlaceID   int64
	Place     Place // where it's kept, with its path
	Name      string
	Unit      string // e.g. "rolls", "boxes"; may be blank
	Quantity  int    // consumables: on hand; reusables: clean and on hand
	ReorderAt int    // low when Quantity <= ReorderAt; 0 = only when out
	Notes     string
	// Reusable supplies (mop heads, rags) are washed instead of used up.
	Reusable bool
	InUse    int // reusables only: currently in use, e.g. on a mop
	Cleaning int // reusables only: out being washed
}

// Total is every unit owned, wherever it is.
func (s Supply) Total() int { return s.Quantity + s.InUse + s.Cleaning }

// Location renders where it's kept: "Main Building › Kitchen".
func (s Supply) Location() string { return s.Place.Path }

// Stock is "out", "low" or "ok".
func (s Supply) Stock() string {
	switch {
	case s.Quantity == 0:
		return "out"
	case s.Quantity <= s.ReorderAt:
		return "low"
	}
	return "ok"
}

// NeedsRestock reports whether the supply is out or low.
func (s Supply) NeedsRestock() bool { return s.Stock() != "ok" }

// StockSummary renders the count for lists: "12 rolls", or for reusables
// "3 clean · 2 in use · 1 cleaning" (zero buckets omitted).
func (s Supply) StockSummary() string {
	if !s.Reusable {
		return s.QuantityLabel()
	}
	out := fmt.Sprintf("%d clean", s.Quantity)
	if s.InUse > 0 {
		out += fmt.Sprintf(" · %d in use", s.InUse)
	}
	if s.Cleaning > 0 {
		out += fmt.Sprintf(" · %d cleaning", s.Cleaning)
	}
	return out
}

// QuantityLabel renders "12 rolls" or just "12".
func (s Supply) QuantityLabel() string {
	if s.Unit == "" {
		return fmt.Sprint(s.Quantity)
	}
	return fmt.Sprintf("%d %s", s.Quantity, s.Unit)
}

type SupplyFilter struct {
	PlaceID int64 // in this place or anywhere inside it
	Direct  bool  // only supplies in PlaceID itself
	Tag     string
	LowOnly bool // only supplies that are out or at/below their reorder level
	Query   string
}

const supplySelect = `
	SELECT s.id, s.place_id, s.name, s.unit, s.quantity, s.reorder_at, s.notes, s.reusable, s.in_use, s.cleaning
	FROM supplies s`

// scanSupplies reads supplies and fills in where they're kept, in tree
// order then by name.
func (st *Store) scanSupplies(rows *sql.Rows) ([]Supply, error) {
	defer rows.Close()
	var out []Supply
	for rows.Next() {
		var s Supply
		if err := rows.Scan(&s.ID, &s.PlaceID, &s.Name, &s.Unit, &s.Quantity, &s.ReorderAt, &s.Notes, &s.Reusable, &s.InUse, &s.Cleaning); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	t, err := loadPlaces(st.DB)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Place, _ = t.Get(out[i].PlaceID)
	}
	slices.SortStableFunc(out, func(a, b Supply) int {
		return cmp.Or(cmp.Compare(a.Place.Order, b.Place.Order), strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	return out, nil
}

// ListSupplies returns supplies in tree order, then by name.
func (s *Store) ListSupplies(f SupplyFilter) ([]Supply, error) {
	q := supplySelect + ` WHERE 1=1`
	var args []any
	q, args = placeFilter(q, args, "s.place_id", f.PlaceID, f.Direct, f.Tag)
	if f.LowOnly {
		q += ` AND (s.quantity = 0 OR s.quantity <= s.reorder_at)`
	}
	if f.Query != "" {
		like := "%" + f.Query + "%"
		q += ` AND (s.name LIKE ? OR s.notes LIKE ? OR s.place_id IN (SELECT id FROM places WHERE name LIKE ? OR number LIKE ?))`
		args = append(args, like, like, like, like)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return s.scanSupplies(rows)
}

func (s *Store) GetSupply(id int64) (*Supply, error) {
	rows, err := s.DB.Query(supplySelect+` WHERE s.id = ?`, id)
	if err != nil {
		return nil, err
	}
	ss, err := s.scanSupplies(rows)
	if err != nil {
		return nil, err
	}
	if len(ss) == 0 {
		return nil, ErrNotFound
	}
	return &ss[0], nil
}

// ErrReusableInCirculation is returned when switching a supply back to
// consumable while some units are still in use or out for cleaning.
var ErrReusableInCirculation = errors.New("some are still in use or out for cleaning")

// SaveSupply creates or updates a supply's details. On create, Quantity
// (and InUse/Cleaning for reusables) are the starting counts and are
// recorded in the history; on update the counts are ignored — use
// AdjustSupply so every change is logged.
func (s *Store) SaveSupply(sp *Supply, userID int64) error {
	sp.Name, sp.Unit = strings.TrimSpace(sp.Name), strings.TrimSpace(sp.Unit)
	if !sp.Reusable {
		sp.InUse, sp.Cleaning = 0, 0
	}
	if sp.ID != 0 {
		// Counts can't be edited here, so check the stored ones.
		res, err := s.DB.Exec(`UPDATE supplies SET place_id = ?, name = ?, unit = ?, reorder_at = ?, notes = ?, reusable = ?
			WHERE id = ? AND (? OR (in_use = 0 AND cleaning = 0))`,
			sp.PlaceID, sp.Name, sp.Unit, sp.ReorderAt, sp.Notes, sp.Reusable, sp.ID, sp.Reusable)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err := s.GetSupply(sp.ID); err != nil {
				return err
			}
			return ErrReusableInCirculation
		}
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertSupplyTx(tx, sp, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// insertSupplyTx adds a new supply and records its starting counts.
func insertSupplyTx(tx *sql.Tx, sp *Supply, userID int64) error {
	res, err := tx.Exec(`INSERT INTO supplies (place_id, name, unit, quantity, reorder_at, notes, reusable, in_use, cleaning)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sp.PlaceID, sp.Name, sp.Unit, sp.Quantity, sp.ReorderAt, sp.Notes, sp.Reusable, sp.InUse, sp.Cleaning)
	if err != nil {
		return err
	}
	if sp.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO supply_changes (supply_id, kind, amount, delta, quantity_after, in_use_after, cleaning_after, created_by)
		VALUES (?, 'added', ?, ?, ?, ?, ?, ?)`,
		sp.ID, sp.Total(), sp.Quantity, sp.Quantity, sp.InUse, sp.Cleaning, nullInt(userID))
	return err
}

func (s *Store) DeleteSupply(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM supplies WHERE id = ?`, id)
	return err
}

// Supply changes ----------------------------------------------------------

type SupplyChange struct {
	ID            int64
	SupplyID      int64
	Kind          string
	Amount        int // how many units the change involved
	Delta         int // change to the (clean) on-hand quantity
	QuantityAfter int
	InUseAfter    int
	CleaningAfter int
	Note          string
	CreatedBy     string
	CreatedAt     string // UTC "YYYY-MM-DD HH:MM:SS"
}

var supplyKindLabels = map[string]string{
	"added":         "Started tracking",
	"used":          "Used",
	"restocked":     "Restocked",
	"counted":       "Recounted",
	"put_in_use":    "Put in use",
	"sent_cleaning": "Sent for cleaning",
	"swapped":       "Swapped dirty for clean",
	"returned":      "Back from cleaning",
	"retired":       "Thrown out",
}

func (c SupplyChange) KindLabel() string {
	if l, ok := supplyKindLabels[c.Kind]; ok {
		return l
	}
	return c.Kind
}

// Adjustment is a request to change a supply's counts. Amount is how many
// units are involved, except for "counted" where it is the new on-hand
// total. "retired" removes units from the From bucket ("stock", "in_use"
// or "cleaning"; default "stock").
type Adjustment struct {
	SupplyID int64
	Kind     string
	Amount   int
	From     string
	Note     string
	UserID   int64
	LogID    int64 // maintenance history entry this was part of, if any
}

// ReusableKinds are the adjustments that only make sense for reusables.
var ReusableKinds = map[string]bool{"put_in_use": true, "sent_cleaning": true, "swapped": true, "returned": true}

// AdjustSupply applies an adjustment and logs it. Taking more out of a
// bucket than it holds returns a *NotEnoughError and changes nothing.
func (s *Store) AdjustSupply(a Adjustment) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := adjustSupplyTx(tx, a); err != nil {
		return err
	}
	return tx.Commit()
}

func adjustSupplyTx(tx *sql.Tx, a Adjustment) error {
	if a.Amount < 0 {
		return fmt.Errorf("negative amount")
	}
	var err error
	var sp Supply
	if err := tx.QueryRow(`SELECT quantity, in_use, cleaning, reusable FROM supplies WHERE id = ?`, a.SupplyID).
		Scan(&sp.Quantity, &sp.InUse, &sp.Cleaning, &sp.Reusable); err != nil {
		return notFound(err)
	}
	if ReusableKinds[a.Kind] && !sp.Reusable {
		return fmt.Errorf("%q only applies to reusable supplies", a.Kind)
	}

	n := a.Amount
	stock, inUse, cleaning := sp.Quantity, sp.InUse, sp.Cleaning
	// take removes n from a bucket, failing if it would go negative.
	take := func(bucket *int, label string) error {
		if *bucket < n {
			return &NotEnoughError{Bucket: label, Have: *bucket}
		}
		*bucket -= n
		return nil
	}
	switch a.Kind {
	case "used":
		err = take(&stock, "on hand")
	case "restocked":
		stock += n
	case "counted":
		stock = n
	case "put_in_use":
		err = take(&stock, "clean on hand")
		inUse += n
	case "sent_cleaning":
		err = take(&inUse, "in use")
		cleaning += n
	case "swapped": // dirty ones off to cleaning, the same number of clean ones on
		if err = take(&inUse, "in use"); err == nil {
			err = take(&stock, "clean on hand")
		}
		inUse += n
		cleaning += n
	case "returned":
		err = take(&cleaning, "out for cleaning")
		stock += n
	case "retired":
		switch a.From {
		case "in_use":
			err = take(&inUse, "in use")
		case "cleaning":
			err = take(&cleaning, "out for cleaning")
		default:
			err = take(&stock, "on hand")
		}
	default:
		return fmt.Errorf("unknown adjustment %q", a.Kind)
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(`UPDATE supplies SET quantity = ?, in_use = ?, cleaning = ? WHERE id = ?`, stock, inUse, cleaning, a.SupplyID); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO supply_changes (supply_id, kind, amount, delta, quantity_after, in_use_after, cleaning_after, note, created_by, log_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.SupplyID, a.Kind, n, stock-sp.Quantity, stock, inUse, cleaning, strings.TrimSpace(a.Note), nullInt(a.UserID), nullInt(a.LogID))
	return err
}

// putBackTx restocks whatever was taken from supplies as part of a
// maintenance history entry that is being deleted.
func putBackTx(tx *sql.Tx, logID, userID int64, note string) error {
	rows, err := tx.Query(`SELECT supply_id, -delta FROM supply_changes WHERE log_id = ? AND kind = 'used'`, logID)
	if err != nil {
		return err
	}
	var giveBack []Adjustment
	for rows.Next() {
		a := Adjustment{Kind: "restocked", Note: note, UserID: userID}
		if err := rows.Scan(&a.SupplyID, &a.Amount); err != nil {
			rows.Close()
			return err
		}
		giveBack = append(giveBack, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range giveBack {
		if err := adjustSupplyTx(tx, a); err != nil {
			return err
		}
	}
	return nil
}

// ListSupplyChanges returns a supply's history, newest first.
func (s *Store) ListSupplyChanges(supplyID int64) ([]SupplyChange, error) {
	rows, err := s.DB.Query(`
		SELECT c.id, c.supply_id, c.kind, c.amount, c.delta, c.quantity_after, c.in_use_after, c.cleaning_after, c.note,
		       COALESCE(NULLIF(u.display_name, ''), u.username, ''), c.created_at
		FROM supply_changes c
		LEFT JOIN users u ON u.id = c.created_by
		WHERE c.supply_id = ?
		ORDER BY c.id DESC`, supplyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SupplyChange
	for rows.Next() {
		var c SupplyChange
		if err := rows.Scan(&c.ID, &c.SupplyID, &c.Kind, &c.Amount, &c.Delta, &c.QuantityAfter, &c.InUseAfter, &c.CleaningAfter, &c.Note,
			&c.CreatedBy, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
