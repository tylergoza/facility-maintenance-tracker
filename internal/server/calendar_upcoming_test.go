package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// upcomingSetup makes an admin, a room "Lifecenter" (place 3) with an item
// whose task is due in two days, and returns the signed-in client.
func upcomingSetup(t *testing.T, env map[string]string) (*client, *store.Store) {
	t.Helper()
	c, st := newTestServerWith(t, Config{Env: env})
	c.post("/setup", "/setup", url.Values{
		"site_name": {"Grace Church"}, "username": {"pastor"},
		"password": {"a long password"}, "password_confirm": {"a long password"},
	}, 200)
	c.post("/places/new", "/places", url.Values{"kind": {"building"}, "name": {"Main"}}, 200)
	c.post("/places/new?parent=2", "/places", url.Values{"kind": {"room"}, "parent_id": {"2"}, "name": {"Lifecenter"}}, 200)
	c.post("/items/new", "/items", url.Values{"place_id": {"3"}, "name": {"Mini-split"}}, 200)
	c.post("/tasks/new?item=1", "/tasks", url.Values{"item_id": {"1"}, "name": {"Replace filter"}}, 200)
	if _, err := st.DB.Exec(`UPDATE tasks SET next_due_on = ? WHERE id = 1`, day(2)); err != nil {
		t.Fatal(err)
	}
	return c, st
}

func day(n int) string { return time.Now().AddDate(0, 0, n).Format(store.DateLayout) }

func TestComingUpHereOffWithoutCalendar(t *testing.T) {
	c, _ := upcomingSetup(t, nil)
	if body := c.get("/places/3", 200); strings.Contains(body, "Coming up here") {
		t.Fatal("section shown with no calendar connection")
	}
}

func TestComingUpHere(t *testing.T) {
	var calls atomic.Int32
	cal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer cal_read1234" || r.URL.Query().Get("room") != "Lifecenter" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"events": [
			{"id": 9, "title": "Fall Dinner", "starts_on": %q, "start_time": "17:30", "end_time": "20:00",
			 "rooms": ["Lifecenter"], "url": "https://cal.example/calendar/9"},
			{"busy": true, "starts_on": %q, "rooms": ["lifecenter"]},
			{"id": 10, "title": "Elsewhere", "starts_on": %q, "rooms": ["Kitchen"]}
		]}`, day(2), day(5), day(2))
	}))
	defer cal.Close()

	c, st := upcomingSetup(t, map[string]string{"CALENDAR_URL": cal.URL, "CALENDAR_TOKEN": "cal_read1234"})
	body := c.get("/places/3", 200)
	asked := calls.Load()
	for _, want := range []string{"Coming up here", "Fall Dinner", "5:30 pm–8:00 pm", "Booked",
		"Replace filter (Mini-split)</a> is due", "when the room is booked"} {
		if !strings.Contains(body, want) {
			t.Errorf("place page lacks %q", want)
		}
	}
	if strings.Contains(body, "Elsewhere") {
		t.Error("another room's booking shown")
	}
	// Cached: a second visit doesn't ask again.
	c.get("/places/3", 200)
	if n := calls.Load(); n != asked {
		t.Errorf("calendar asked again within a minute (%d calls, want %d)", n, asked)
	}

	// Overdue work is checked against today; nothing booked today here.
	st.DB.Exec(`UPDATE tasks SET next_due_on = ? WHERE id = 1`, day(-3))
	if body := c.get("/places/3", 200); strings.Contains(body, "when the room is booked") || strings.Contains(body, "is overdue") {
		t.Error("overdue task warned though nothing is booked today")
	}
}

func TestComingUpHereCalendarDown(t *testing.T) {
	c, _ := upcomingSetup(t, map[string]string{"CALENDAR_URL": "http://127.0.0.1:1", "CALENDAR_TOKEN": "cal_read1234"})
	if body := c.get("/places/3", 200); !strings.Contains(body, "Couldn&#39;t ask the calendar") {
		t.Fatal("expected the calendar-down note")
	}
}
