package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// Products ---------------------------------------------------------------

func (s *Server) handleProducts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ProductFilter{PlaceID: queryInt(r, "place"), Tag: q.Get("tag"), Query: q.Get("q"), Category: q.Get("category"),
		CountedOnly: q.Get("counted") == "1", PortableOnly: q.Get("portable") == "1"}
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
	s.render(w, r, http.StatusOK, "products/show", map[string]any{"Title": p.Name, "Product": p, "Items": items})
}

func (s *Server) renderProductForm(w http.ResponseWriter, r *http.Request, status int, p *store.Product, errs []string) {
	cats, _ := s.store.Categories()
	all, err := s.store.ListProducts(store.ProductFilter{})
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
	s.render(w, r, status, "products/form", map[string]any{
		"Title": "Edit " + p.Name, "Form": p, "Categories": cats, "Others": others, "Errors": errs,
	})
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
	p.Name, p.Category, p.Notes = formStr(r, "name"), formStr(r, "category"), formStr(r, "notes")
	p.Counted = r.PostFormValue("tracking") == "count"
	var errs []string
	if p.Name == "" {
		errs = append(errs, "Name is required.")
	}
	errs = append(errs, tooLong("Name", p.Name, 100)...)
	if errs != nil {
		s.renderProductForm(w, r, http.StatusUnprocessableEntity, p, errs)
		return
	}
	if err := s.store.SaveProduct(p); errors.Is(err, store.ErrProductExists) {
		s.renderProductForm(w, r, http.StatusUnprocessableEntity, p, []string{
			"There's already a product called " + p.Name + ". To make them one, merge them below."})
		return
	} else if err != nil {
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
