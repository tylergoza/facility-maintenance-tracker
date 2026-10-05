package store

import (
	"database/sql"
	"errors"
	"strings"
)

// ErrProductExists is returned when renaming a product to the name of
// another one; merge them instead.
var ErrProductExists = errors.New("another product has that name")

// Product is what an item is: "Folding chairs", "Lapel mics", "Lights".
// The same product in many places is many items, so they add up.
type Product struct {
	ID       int64
	Name     string
	Category string
	// Counted products (chairs, tables) are only counted: their items
	// have no numbered units, IDs or notes. Others are tracked one by one.
	Counted bool
	Notes   string
	// Filled in by ListProducts and GetProduct, over the items they
	// looked at: how many there are, in how many items and places.
	Total      int
	ItemCount  int
	PlaceCount int
}

// Tracking renders how its items are kept: "Counted" or "One by one".
func (p Product) Tracking() string {
	if p.Counted {
		return "Counted"
	}
	return "One by one"
}

type ProductFilter struct {
	Query    string // name, category or notes
	Category string
	// Only count items here (or inside it) or in places with this tag;
	// products with none there are left out.
	PlaceID int64
	Tag     string
	// Only products whose items move, or that are counted.
	PortableOnly bool
	CountedOnly  bool
}

// ListProducts returns products by name with their totals. A place or
// tag filter totals only the items there.
func (s *Store) ListProducts(f ProductFilter) ([]Product, error) {
	where, args := `1=1`, []any{}
	where, args = placeFilter(where, args, "i.place_id", f.PlaceID, false, f.Tag)
	if f.PortableOnly {
		where += ` AND i.portable = 1`
	}
	q := `
		SELECT p.id, p.name, p.category, p.counted, p.notes,
		       COALESCE(SUM(i.quantity), 0), COUNT(i.id), COUNT(DISTINCT i.place_id)
		FROM products p
		LEFT JOIN items i ON i.product_id = p.id AND ` + where + `
		WHERE 1=1`
	if f.Query != "" {
		like := "%" + f.Query + "%"
		q += ` AND (p.name LIKE ? OR p.category LIKE ? OR p.notes LIKE ?)`
		args = append(args, like, like, like)
	}
	if f.Category != "" {
		q += ` AND p.category = ? COLLATE NOCASE`
		args = append(args, f.Category)
	}
	if f.CountedOnly {
		q += ` AND p.counted = 1`
	}
	q += ` GROUP BY p.id`
	if f.PlaceID != 0 || f.Tag != "" || f.PortableOnly {
		q += ` HAVING COUNT(i.id) > 0`
	}
	q += ` ORDER BY p.name COLLATE NOCASE`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Category, &p.Counted, &p.Notes, &p.Total, &p.ItemCount, &p.PlaceCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProduct(id int64) (*Product, error) {
	var p Product
	err := s.DB.QueryRow(`
		SELECT p.id, p.name, p.category, p.counted, p.notes,
		       COALESCE(SUM(i.quantity), 0), COUNT(i.id), COUNT(DISTINCT i.place_id)
		FROM products p LEFT JOIN items i ON i.product_id = p.id
		WHERE p.id = ? GROUP BY p.id`, id).
		Scan(&p.ID, &p.Name, &p.Category, &p.Counted, &p.Notes, &p.Total, &p.ItemCount, &p.PlaceCount)
	if err != nil {
		return nil, notFound(err)
	}
	return &p, nil
}

// ProductByName finds a product by name, ignoring case.
func (s *Store) ProductByName(name string) (*Product, error) {
	var id int64
	if err := s.DB.QueryRow(`SELECT id FROM products WHERE name = ?`, strings.TrimSpace(name)).Scan(&id); err != nil {
		return nil, notFound(err)
	}
	return s.GetProduct(id)
}

// SaveProduct updates a product's details. Switching it to counted drops
// its items' unit IDs and notes, and which units history and problems
// were about; switching back numbers them afresh.
func (s *Store) SaveProduct(p *Product) error {
	p.Name, p.Category = strings.TrimSpace(p.Name), strings.TrimSpace(p.Category)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var was bool
	if err := tx.QueryRow(`SELECT counted FROM products WHERE id = ?`, p.ID).Scan(&was); err != nil {
		return notFound(err)
	}
	var other int64
	if err := tx.QueryRow(`SELECT id FROM products WHERE name = ? AND id != ?`, p.Name, p.ID).Scan(&other); err == nil {
		return ErrProductExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(`UPDATE products SET name = ?, category = ?, counted = ?, notes = ? WHERE id = ?`,
		p.Name, p.Category, p.Counted, p.Notes, p.ID); err != nil {
		return err
	}
	if p.Counted != was {
		if err := refreshUnits(tx, `product_id = ?`, p.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MergeProduct makes every item of product from an item of product into,
// and removes from. Units of portable items whose IDs are already used
// among into's are given new ones. into keeps its details, filling in a
// blank category or notes from from's.
func (s *Store) MergeProduct(from, into int64) error {
	if from == into {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fromCounted, intoCounted bool
	var name string
	if err := tx.QueryRow(`SELECT counted FROM products WHERE id = ?`, from).Scan(&fromCounted); err != nil {
		return notFound(err)
	}
	if err := tx.QueryRow(`SELECT counted, name FROM products WHERE id = ?`, into).Scan(&intoCounted, &name); err != nil {
		return notFound(err)
	}
	moving, err := idList(tx, `SELECT id FROM items WHERE product_id = ? ORDER BY id`, from)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE products SET
		category = CASE WHEN category = '' THEN (SELECT category FROM products WHERE id = ?) ELSE category END,
		notes = CASE WHEN notes = '' THEN (SELECT notes FROM products WHERE id = ?) ELSE notes END
		WHERE id = ?`, from, from, into); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE items SET product_id = ? WHERE product_id = ?`, into, from); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM products WHERE id = ?`, from); err != nil {
		return err
	}
	for _, id := range moving {
		switch {
		case intoCounted:
			err = refreshUnits(tx, `id = ?`, id)
		case fromCounted:
			err = fillUnitIDs(tx, id)
		default:
			err = freeClashingIDs(tx, id, name)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// productFor finds the product named name, or adds one with the category
// and counting given. It returns the product as it is.
func productFor(tx *sql.Tx, name, category string, counted bool) (Product, error) {
	p := Product{Name: strings.TrimSpace(name)}
	err := tx.QueryRow(`SELECT id, name, category, counted, notes FROM products WHERE name = ?`, p.Name).
		Scan(&p.ID, &p.Name, &p.Category, &p.Counted, &p.Notes)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.Category, p.Counted = strings.TrimSpace(category), counted
	res, err := tx.Exec(`INSERT INTO products (name, category, counted) VALUES (?, ?, ?)`, p.Name, p.Category, p.Counted)
	if err != nil {
		return p, err
	}
	p.ID, err = res.LastInsertId()
	return p, err
}

// dropEmptyProducts removes products no item is left in.
func dropEmptyProducts(tx *sql.Tx) error {
	_, err := tx.Exec(`DELETE FROM products WHERE id NOT IN (SELECT product_id FROM items)`)
	return err
}

// refreshUnits brings the units of the items matching where (one ? arg)
// in line with whether their product is counted: counted ones lose their
// units, and history and problems forget which unit they were about;
// tracked ones are numbered.
func refreshUnits(tx *sql.Tx, where string, arg any) error {
	ids, err := idList(tx, `SELECT id FROM items WHERE `+where+` ORDER BY id`, arg)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var counted bool
		if err := tx.QueryRow(`SELECT p.counted FROM items i JOIN products p ON p.id = i.product_id WHERE i.id = ?`, id).Scan(&counted); err != nil {
			return err
		}
		if !counted {
			if err := fillUnitIDs(tx, id); err != nil {
				return err
			}
			continue
		}
		for _, q := range []string{
			`DELETE FROM item_units WHERE item_id = ?`,
			`DELETE FROM log_units WHERE log_id IN (SELECT id FROM maintenance_logs WHERE item_id = ?)`,
			`UPDATE problems SET unit = NULL WHERE item_id = ?`,
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// freeClashingIDs gives new IDs to an item's units whose IDs others of
// its kind (named name) already use.
func freeClashingIDs(tx *sql.Tx, itemID int64, name string) error {
	var portable bool
	if err := tx.QueryRow(`SELECT portable FROM items WHERE id = ?`, itemID).Scan(&portable); err != nil {
		return notFound(err)
	}
	taken, err := unitIDsTaken(tx, itemID, name, portable)
	if err != nil {
		return err
	}
	for tag := range taken {
		if _, err := tx.Exec(`UPDATE item_units SET tag = '' WHERE item_id = ? AND lower(tag) = ?`, itemID, tag); err != nil {
			return err
		}
	}
	return fillUnitIDs(tx, itemID)
}

// idList runs a query selecting one id column.
func idList(q querier, query string, args ...any) ([]int64, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
