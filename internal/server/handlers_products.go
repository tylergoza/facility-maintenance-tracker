package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// Products ---------------------------------------------------------------

func (s *Server) handleProducts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ProductFilter{PlaceID: queryInt(r, "place"), Tag: q.Get("tag"), Query: q.Get("q"), Category: q.Get("category"),
		CountedOnly: q.Get("counted") == "1", PortableOnly: q.Get("portable") == "1", BookingOnly: q.Get("booking") == "1"}
	products, err := s.store.ListProducts(f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tree, err := s.store.Places()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cats, _ := s.store.Categories()
	s.render(w, r, http.StatusOK, "products/index", map[string]any{
		"Title": "Products", "Products": products, "Places": tree.All(), "Tags": tree.Tags(), "Categories": cats, "Filter": f,
	})
}

func (s *Server) handleProductShow(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProduct(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	items, err := s.store.ListItems(store.ItemFilter{ProductID: p.ID})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var stuck []store.Item
	if p.Booking == store.BookingCount {
		if stuck, err = s.store.NonPortableItems(p.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.render(w, r, http.StatusOK, "products/show", map[string]any{"Title": p.Name, "Product": p, "Items": items, "NonPortable": stuck})
}

// offerRow is one bookable place in the "Offered with" list.
type offerRow struct {
	ID    int64
	Label string
	Whole bool // a building offered as a whole
}

type offerGroup struct {
	Building string
	Places   []offerRow
}

// offerGroups lists the bookable places grouped by building.
func (s *Server) offerGroups() ([]offerGroup, error) {
	tree, err := s.store.Places()
	if err != nil {
		return nil, err
	}
	var groups []offerGroup
	at := map[int64]int{}
	for _, pl := range tree.Bookable() {
		b, _ := tree.Building(pl.ID)
		i, ok := at[b.ID]
		if !ok {
			i = len(groups)
			at[b.ID] = i
			groups = append(groups, offerGroup{Building: b.Label()})
		}
		row := offerRow{ID: pl.ID, Label: pl.Label()}
		if pl.Kind == store.KindBuilding {
			row.Label += " (the whole building)"
			row.Whole = true
		}
		groups[i].Places = append(groups[i].Places, row)
	}
	return groups, nil
}

func (s *Server) renderProductForm(w http.ResponseWriter, r *http.Request, status int, p *store.Product, errs []string) {
	cats, _ := s.store.Categories()
	all, err := s.store.ListProducts(store.ProductFilter{})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	groups, err := s.offerGroups()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var others []store.Product
	for _, o := range all {
		if o.ID != p.ID {
			others = append(others, o)
		}
	}
	title := "Edit " + p.Name
	if p.ID == 0 {
		title = "New product"
	}
	s.render(w, r, status, "products/form", map[string]any{
		"Title": title, "Form": p, "Categories": cats, "Others": others, "Errors": errs, "OfferGroups": groups,
	})
}

func (s *Server) handleProductNew(w http.ResponseWriter, r *http.Request) {
	s.renderProductForm(w, r, http.StatusOK, &store.Product{}, nil)
}

// productFromForm reads the product form into p. With the calendar
// setting off the stored offers are left as they are, so turning it back
// on finds them again.
func productFromForm(r *http.Request, p *store.Product) []string {
	p.Name, p.Category, p.Notes = formStr(r, "name"), formStr(r, "category"), formStr(r, "notes")
	p.Counted = r.PostFormValue("tracking") == "count"
	p.Booking = r.PostFormValue("booking")
	var errs []string
	if p.Booking != "" && p.Booking != store.BookingCount && p.Booking != store.BookingService {
		p.Booking = ""
		errs = append(errs, "Choose how the calendar asks for it.")
	}
	if p.Booking != "" {
		p.Offers = nil
		for _, v := range r.PostForm["offer"] {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				p.Offers = append(p.Offers, id)
			}
		}
	}
	if p.Name == "" {
		errs = append(errs, "Name is required.")
	}
	return append(errs, tooLong("Name", p.Name, 100)...)
}

// productSaveError turns a save error into form errors; ok is false when
// it is some other error.
func productSaveError(err error, name string) (msgs []string, ok bool) {
	switch {
	case errors.Is(err, store.ErrProductExists):
		return []string{"There's already a product called " + name + ". To make them one, merge them from that product's page."}, true
	case errors.Is(err, store.ErrOfferNotBookable):
		return []string{"One of the places you ticked can't be booked any more. Choose from the list again."}, true
	}
	return nil, false
}

func (s *Server) handleProductCreate(w http.ResponseWriter, r *http.Request) {
	p := &store.Product{}
	if errs := productFromForm(r, p); errs != nil {
		s.renderProductForm(w, r, http.StatusUnprocessableEntity, p, errs)
		return
	}
	if err := s.store.CreateProduct(p); err != nil {
		if msgs, ok := productSaveError(err, p.Name); ok {
			s.renderProductForm(w, r, http.StatusUnprocessableEntity, p, msgs)
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/products/%d", p.ID), p.Name+" added.")
}

func (s *Server) handleProductEdit(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProduct(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderProductForm(w, r, http.StatusOK, p, nil)
}

func (s *Server) handleProductUpdate(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProduct(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if errs := productFromForm(r, p); errs != nil {
		s.renderProductForm(w, r, http.StatusUnprocessableEntity, p, errs)
		return
	}
	if err := s.store.SaveProduct(p); err != nil {
		if msgs, ok := productSaveError(err, p.Name); ok {
			s.renderProductForm(w, r, http.StatusUnprocessableEntity, p, msgs)
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/products/%d", p.ID), p.Name+" saved.")
}

func (s *Server) handleProductMerge(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProduct(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	into, err := s.store.GetProduct(formInt(r, "into"))
	if err != nil || into.ID == p.ID {
		s.renderProductForm(w, r, http.StatusUnprocessableEntity, p, []string{"Choose the product to merge it into."})
		return
	}
	if err := s.store.MergeProduct(p.ID, into.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/products/%d", into.ID), fmt.Sprintf("%s merged into %s.", p.Name, into.Name))
}
