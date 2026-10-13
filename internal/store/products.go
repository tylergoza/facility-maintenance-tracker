package store

import (
	"database/sql"
	"errors"
	"strings"
)

// ErrProductExists is returned when renaming a product to the name of
// another one; merge them instead.
var ErrProductExists = errors.New("another product has that name")

// ErrOfferNotBookable is returned when a product is offered with a place
// that isn't marked bookable.
var ErrOfferNotBookable = errors.New("products can only be offered with bookable places")

// How a product is asked for with a room booking. The empty string means
// it isn't offered.
const (
	BookingCount   = "count"   // ask how many
	BookingService = "service" // ask yes or no
)

func validBooking(b string) bool {
	return b == "" || b == BookingCount || b == BookingService
}

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
	// How it is asked for on the church calendar ("" = not offered), and
	// the bookable places it is offered with. Offers on places that
	// stopped being bookable are kept but not loaded.
	Booking string
	Offers  []int64
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

// BookingLabel renders the booking setting for people.
func (p Product) BookingLabel() string {
	switch p.Booking {
	case BookingCount:
		return "Ask how many"
	case BookingService:
		return "Ask yes or no"
	}
	return "Not offered"
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
	// Only products set to be asked for that are offered with at least
	// one bookable place.
	BookingOnly bool
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
		SELECT p.id, p.name, p.category, p.counted, p.notes, p.booking,
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
	if f.BookingOnly {
		q += ` AND p.booking != '' AND EXISTS (SELECT 1 FROM product_offers o JOIN places pl ON pl.id = o.place_id
			WHERE o.product_id = p.id AND pl.bookable = 1)`
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
		if err := rows.Scan(&p.ID, &p.Name, &p.Category, &p.Counted, &p.Notes, &p.Booking, &p.Total, &p.ItemCount, &p.PlaceCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	offers, err := s.offersByProduct()
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Offers = offers[out[i].ID]
	}
	return out, nil
}

// offersByProduct loads every offer on a bookable place, by product.
func (s *Store) offersByProduct() (map[int64][]int64, error) {
	rows, err := s.DB.Query(`SELECT o.product_id, o.place_id FROM product_offers o
		JOIN places pl ON pl.id = o.place_id WHERE pl.bookable = 1 ORDER BY o.product_id, o.place_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var pid, place int64
		if err := rows.Scan(&pid, &place); err != nil {
			return nil, err
		}
		out[pid] = append(out[pid], place)
	}
	return out, rows.Err()
}

func (s *Store) GetProduct(id int64) (*Product, error) {
	var p Product
	err := s.DB.QueryRow(`
		SELECT p.id, p.name, p.category, p.counted, p.notes, p.booking,
		       COALESCE(SUM(i.quantity), 0), COUNT(i.id), COUNT(DISTINCT i.place_id)
		FROM products p LEFT JOIN items i ON i.product_id = p.id
		WHERE p.id = ? GROUP BY p.id`, id).
		Scan(&p.ID, &p.Name, &p.Category, &p.Counted, &p.Notes, &p.Booking, &p.Total, &p.ItemCount, &p.PlaceCount)
	if err != nil {
		return nil, notFound(err)
	}
	p.Offers, err = idList(s.DB, `SELECT o.place_id FROM product_offers o
		JOIN places pl ON pl.id = o.place_id WHERE o.product_id = ? AND pl.bookable = 1 ORDER BY o.place_id`, id)
	if err != nil {
		return nil, err
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

// writeOffers replaces the product's offers on bookable places with
// offers, leaving rows on places that aren't bookable alone.
func writeOffers(tx *sql.Tx, productID int64, offers []int64) error {
	for _, id := range offers {
		var ok bool
		if err := tx.QueryRow(`SELECT bookable FROM places WHERE id = ?`, id).Scan(&ok); err != nil || !ok {
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			return ErrOfferNotBookable
		}
	}
	if _, err := tx.Exec(`DELETE FROM product_offers WHERE product_id = ?
		AND place_id IN (SELECT id FROM places WHERE bookable = 1)`, productID); err != nil {
		return err
	}
	for _, id := range offers {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO product_offers (product_id, place_id) VALUES (?, ?)`, productID, id); err != nil {
			return err
		}
	}
	return nil
}

// CreateProduct adds a product with no items, e.g. a service like
// Sound. It sets p.ID.
func (s *Store) CreateProduct(p *Product) error {
	p.Name, p.Category = strings.TrimSpace(p.Name), strings.TrimSpace(p.Category)
	if p.Name == "" {
		return errors.New("a product needs a name")
	}
	if !validBooking(p.Booking) {
		return errors.New("unknown booking setting")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var other int64
	if err := tx.QueryRow(`SELECT id FROM products WHERE name = ?`, p.Name).Scan(&other); err == nil {
		return ErrProductExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	res, err := tx.Exec(`INSERT INTO products (name, category, counted, notes, booking) VALUES (?, ?, ?, ?, ?)`,
		p.Name, p.Category, p.Counted, p.Notes, p.Booking)
	if err != nil {
		return err
	}
	if p.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	if err := writeOffers(tx, p.ID, p.Offers); err != nil {
		return err
	}
	return tx.Commit()
}

// NonPortableItems returns the product's items that can't be moved;
// "Ask how many" needs them all to be portable.
func (s *Store) NonPortableItems(productID int64) ([]Item, error) {
	items, err := s.ListItems(ItemFilter{ProductID: productID})
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, it := range items {
		if !it.Portable {
			out = append(out, it)
		}
	}
	return out, nil
}

// ProductsOfferedAt returns the products set to be asked for that have an
// offer on the place, whether or not it is bookable now.
func (s *Store) ProductsOfferedAt(placeID int64) ([]Product, error) {
	rows, err := s.DB.Query(`SELECT p.id, p.name, p.category, p.counted, p.notes, p.booking
		FROM products p JOIN product_offers o ON o.product_id = p.id
		WHERE o.place_id = ? AND p.booking != '' ORDER BY p.name COLLATE NOCASE`, placeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Category, &p.Counted, &p.Notes, &p.Booking); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SaveProduct updates a product's details. Switching it to counted drops
// its items' unit IDs and notes, and which units history and problems
// were about; switching back numbers them afresh. Its offers on bookable
// places are replaced with p.Offers (ErrOfferNotBookable if any isn't
// bookable); clearing Booking keeps them stored.
func (s *Store) SaveProduct(p *Product) error {
	p.Name, p.Category = strings.TrimSpace(p.Name), strings.TrimSpace(p.Category)
	if !validBooking(p.Booking) {
		return errors.New("unknown booking setting")
	}
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
	if _, err := tx.Exec(`UPDATE products SET name = ?, category = ?, counted = ?, notes = ?, booking = ? WHERE id = ?`,
		p.Name, p.Category, p.Counted, p.Notes, p.Booking, p.ID); err != nil {
		return err
	}
	if err := writeOffers(tx, p.ID, p.Offers); err != nil {
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
// blank category or notes from from's. from's offers carry over, and its
// booking setting if into has none.
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
	if _, err := tx.Exec(`INSERT OR IGNORE INTO product_offers (product_id, place_id)
		SELECT ?, place_id FROM product_offers WHERE product_id = ?`, into, from); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE products SET booking = (SELECT booking FROM products WHERE id = ?)
		WHERE id = ? AND booking = ''`, from, into); err != nil {
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

// dropEmptyProducts removes products no item is left in, except ones set
// to be asked for on the calendar: a service like Sound has no items.
func dropEmptyProducts(tx *sql.Tx) error {
	_, err := tx.Exec(`DELETE FROM products WHERE booking = '' AND id NOT IN (SELECT product_id FROM items)`)
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
