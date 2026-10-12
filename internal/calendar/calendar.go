// Package calendar reads bookings from the church calendar (../calendar),
// so a room's page can show what's coming up there. It only reads: the
// tracker owns the rooms and the calendar imports them, never the other
// way round. Authorised with a read token (cal_…) made on the calendar's
// API tokens page.
package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotConfigured means there's no calendar address or token.
var ErrNotConfigured = errors.New("the calendar isn't set up; an admin can add it on Connections")

type Client struct {
	// PublicURL is the address people's browsers use (links); InternalURL
	// is for server-to-server calls (PublicURL if blank).
	PublicURL   string
	InternalURL string
	Token       string
	HTTP        *http.Client
}

func New(publicURL, internalURL, token string) *Client {
	pub := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	in := strings.TrimRight(strings.TrimSpace(internalURL), "/")
	if in == "" {
		in = pub
	}
	return &Client{PublicURL: pub, InternalURL: in, Token: strings.TrimSpace(token),
		HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) Configured() bool { return c != nil && c.PublicURL != "" && c.Token != "" }

// Event is a booking as the calendar's read API shows it: in full, or
// only its rooms and times when Busy (a private or locked booking the
// token can't see).
type Event struct {
	ID        int64    `json:"id"`
	Busy      bool     `json:"busy"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	StartsOn  string   `json:"starts_on"`
	StartTime string   `json:"start_time"`
	EndsOn    string   `json:"ends_on"`
	EndTime   string   `json:"end_time"`
	Rooms     []string `json:"rooms"`
	URL       string   `json:"url"`
	Source    string   `json:"source"`
}

// LastDay is the event's last day (YYYY-MM-DD).
func (e Event) LastDay() string {
	if e.EndsOn != "" {
		return e.EndsOn
	}
	return e.StartsOn
}

// On reports whether the event takes up any of day (YYYY-MM-DD).
func (e Event) On(day string) bool { return e.StartsOn <= day && day <= e.LastDay() }

// InRoom reports whether the event is in a room with one of names
// (compared without case).
func (e Event) InRoom(names ...string) bool {
	for _, r := range e.Rooms {
		for _, n := range names {
			if n != "" && strings.EqualFold(strings.TrimSpace(r), strings.TrimSpace(n)) {
				return true
			}
		}
	}
	return false
}

// Error is the calendar answering with something other than success.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Status == http.StatusUnauthorized {
		return "the calendar turned down the token; make a new read token on its API tokens page"
	}
	if e.Message != "" {
		return fmt.Sprintf("the calendar said: %s (HTTP %d)", e.Message, e.Status)
	}
	return fmt.Sprintf("the calendar answered HTTP %d", e.Status)
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	u := c.InternalURL + "/api/v1" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach the calendar: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(raw, &e)
		return &Error{Status: resp.StatusCode, Message: e.Error}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("the calendar sent something unexpected (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// Events is the bookings in a room between from and to (YYYY-MM-DD,
// inclusive), cancelled ones left out. room is sent as the calendar's
// ?room= filter; the answer is filtered again by names (the place's name
// and its "104 – Nursery" label), so a calendar that ignores the filter
// can't show another room's bookings.
func (c *Client) Events(ctx context.Context, from, to, room string, names ...string) ([]Event, error) {
	var out struct {
		Events []Event `json:"events"`
	}
	if err := c.get(ctx, "/events", url.Values{"from": {from}, "to": {to}, "room": {room}}, &out); err != nil {
		return nil, err
	}
	names = append(names, room)
	var keep []Event
	for _, e := range out.Events {
		if e.Status != "cancelled" && e.InRoom(names...) {
			keep = append(keep, e)
		}
	}
	return keep, nil
}

// Check sees that the address and token work.
func (c *Client) Check(ctx context.Context) error {
	today := time.Now().Format("2006-01-02")
	var out json.RawMessage
	return c.get(ctx, "/events", url.Values{"from": {today}, "to": {today}}, &out)
}
