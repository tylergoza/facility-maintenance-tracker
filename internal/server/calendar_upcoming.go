package server

import (
	"context"
	"sync"
	"time"

	"github.com/tylergoza/facility-maintenance-tracker/internal/calendar"
	"github.com/tylergoza/facility-maintenance-tracker/internal/store"
)

// Coming up here ------------------------------------------------------------
//
// With the calendar connection set, a place's page shows the next two
// weeks of bookings in it, and maintenance due on a booked day there gets
// a warning. Off (nothing shown) without the connection. The calendar is
// asked at most once a minute per place, and never for more than a few
// seconds, so a slow calendar can't hold up the page.

const (
	upcomingDays    = 14
	upcomingTTL     = time.Minute
	upcomingTimeout = 3 * time.Second
)

type upcomingEntry struct {
	at     time.Time
	events []calendar.Event
	err    error
}

type upcomingCache struct {
	mu sync.Mutex
	m  map[int64]upcomingEntry
}

func newUpcomingCache() *upcomingCache { return &upcomingCache{m: map[int64]upcomingEntry{}} }

// clear forgets everything, for when the connection changes.
func (c *upcomingCache) clear() {
	c.mu.Lock()
	c.m = map[int64]upcomingEntry{}
	c.mu.Unlock()
}

// upcomingBooking is one booking as the place page shows it.
type upcomingBooking struct {
	When  string // "Sat, Nov 7 · 5:30–8:00 pm"
	Title string // "Booked" for busy blocks
	URL   string
}

// bookedTask is maintenance due on a day the room is booked.
type bookedTask struct {
	Task    store.Task
	Day     string // the booked day it's due (today when overdue)
	Overdue bool
	Title   string // what the room is booked for ("Booked" when busy)
}

type upcomingView struct {
	Bookings []upcomingBooking
	Tasks    []bookedTask
	Err      string // the calendar couldn't be asked
	Link     string // the calendar's own page
}

// upcomingEvents asks the calendar (or the cache) for p's bookings over the
// next two weeks. ok is false when the calendar connection is off.
func (s *Server) upcomingEvents(ctx context.Context, p store.Place) (events []calendar.Event, err error, ok bool) {
	cal := s.Calendar()
	if !cal.Configured() {
		return nil, nil, false
	}
	s.upcoming.mu.Lock()
	e, hit := s.upcoming.m[p.ID]
	s.upcoming.mu.Unlock()
	if hit && time.Since(e.at) < upcomingTTL {
		return e.events, e.err, true
	}
	ctx, cancel := context.WithTimeout(ctx, upcomingTimeout)
	defer cancel()
	from := s.todayTime()
	to := from.AddDate(0, 0, upcomingDays-1)
	events, err = cal.Events(ctx, from.Format(store.DateLayout), to.Format(store.DateLayout), p.Name, p.Label())
	if err != nil {
		s.log.Warn("calendar: bookings for a place", "place", p.ID, "err", err)
	}
	s.upcoming.mu.Lock()
	s.upcoming.m[p.ID] = upcomingEntry{at: time.Now(), events: events, err: err}
	s.upcoming.mu.Unlock()
	return events, err, true
}

// upcomingFor builds the place page's "Coming up here" section, or nil
// when the calendar connection is off. tasks are the active maintenance
// tasks in p and inside it.
func (s *Server) upcomingFor(ctx context.Context, p store.Place, tasks []store.Task) *upcomingView {
	events, err, ok := s.upcomingEvents(ctx, p)
	if !ok {
		return nil
	}
	v := &upcomingView{Link: s.Calendar().PublicURL}
	if err != nil {
		v.Err = "Couldn't ask the calendar what's booked here right now."
		return v
	}
	for _, e := range events {
		b := upcomingBooking{When: bookingWhen(e), Title: e.Title, URL: e.URL}
		if e.Busy || b.Title == "" {
			b.Title, b.URL = "Booked", ""
		}
		v.Bookings = append(v.Bookings, b)
	}
	last := s.todayTime().AddDate(0, 0, upcomingDays-1).Format(store.DateLayout)
	for _, t := range tasks {
		if t.NextDueOn == "" || t.NextDueOn > last {
			continue
		}
		// Overdue work is planned for today.
		day := max(t.NextDueOn, s.today())
		for k, e := range events {
			if e.On(day) {
				v.Tasks = append(v.Tasks, bookedTask{Task: t, Day: day, Overdue: t.NextDueOn < day, Title: v.Bookings[k].Title})
				break
			}
		}
	}
	return v
}

// bookingWhen reads "Sat, Nov 7 · 5:30–8:00 pm", "Sat, Nov 7 – Mon, Nov 9"
// or "Sat, Nov 7 · all day".
func bookingWhen(e calendar.Event) string {
	day := func(d string) string {
		t, err := time.Parse(store.DateLayout, d)
		if err != nil {
			return d
		}
		return t.Format("Mon, Jan 2")
	}
	out := day(e.StartsOn)
	if e.EndsOn != "" && e.EndsOn != e.StartsOn {
		out += " – " + day(e.EndsOn)
	}
	switch {
	case e.StartTime == "":
		if e.EndsOn == "" || e.EndsOn == e.StartsOn {
			out += " · all day"
		}
	case e.EndTime == "":
		out += " · " + clock(e.StartTime)
	default:
		out += " · " + clock(e.StartTime) + "–" + clock(e.EndTime)
	}
	return out
}

// clock turns "17:30" into "5:30 pm".
func clock(hm string) string {
	t, err := time.Parse("15:04", hm)
	if err != nil {
		return hm
	}
	return t.Format("3:04 pm")
}
