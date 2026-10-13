package calendar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeCalendar(t *testing.T, queries *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*queries = append(*queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer cal_good" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"bad token"}`))
			return
		}
		if r.URL.Path != "/api/v1/events" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Ignores ?room= on purpose: the client filters again.
		w.Write([]byte(`{"events": [
			{"id": 3, "source": "events", "title": "Fall Dinner", "status": "confirmed", "starts_on": "2026-11-07",
			 "start_time": "17:30", "end_time": "20:00", "rooms": ["lifecenter", "Kitchen"], "url": "https://cal/3"},
			{"busy": true, "starts_on": "2026-11-08", "ends_on": "2026-11-09", "rooms": ["104 – Nursery"]},
			{"id": 5, "title": "Called off", "status": "cancelled", "starts_on": "2026-11-10", "rooms": ["Lifecenter"]},
			{"id": 6, "title": "Elsewhere", "starts_on": "2026-11-11", "rooms": ["Pavilion"]}
		]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEvents(t *testing.T) {
	var q []string
	srv := fakeCalendar(t, &q)
	c := New("https://calendar.example", srv.URL, "cal_good")

	got, err := c.Events(context.Background(), "2026-11-01", "2026-11-14", "Lifecenter")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("got %+v, want only the dinner (cancelled and other rooms left out)", got)
	}
	if q[0] != "from=2026-11-01&room=Lifecenter&to=2026-11-14" {
		t.Fatalf("query %q", q[0])
	}

	got, err = c.Events(context.Background(), "2026-11-01", "2026-11-14", "Nursery", "104 – Nursery")
	if err != nil || len(got) != 1 || !got[0].Busy || !got[0].On("2026-11-09") || got[0].On("2026-11-10") {
		t.Fatalf("by label: %+v, %v", got, err)
	}
}

func TestErrors(t *testing.T) {
	var q []string
	srv := fakeCalendar(t, &q)
	if err := New(srv.URL, "", "cal_good").Check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}
	err := New(srv.URL, "", "cal_bad").Check(context.Background())
	var ce *Error
	if !errors.As(err, &ce) || ce.Status != http.StatusUnauthorized {
		t.Fatalf("bad token: %v", err)
	}
	if err := New("", "", "").Check(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("not configured: %v", err)
	}
	if New("https://cal.example", "", "").Configured() {
		t.Fatal("configured with no token")
	}
}
