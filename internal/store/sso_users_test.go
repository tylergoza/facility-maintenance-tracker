package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Migration 016 rebuilds users; with foreign keys off the rebuild must not
// cascade into sessions or set any created_by / assigned_to / reported_by /
// requested_by column to NULL, and ids stay the same.
func TestSSOMigrationKeepsReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v15.db")
	old, err := openAt(path, 15)
	if err != nil {
		t.Fatal(err)
	}
	filler, _ := old.CreateUser("filler", "", "a long password", false)
	old.DeleteUser(filler) // so ids don't just count from 1
	annID, err := old.CreateUser("Ann", "Ann A", "a long password", true)
	must(t, err)
	bobID, err := old.CreateUser("bob", "", "a long password", false)
	must(t, err)

	b := building(t, old, "Sanctuary")
	item := &Item{PlaceID: b.ID, Name: "Furnace"}
	must(t, old.SaveItem(item, nil, 0))
	must(t, old.RecordMaintenance(Completion{ItemID: item.ID, PerformedOn: "2026-09-15"}))
	sp := &Supply{PlaceID: b.ID, Name: "Filters", Quantity: 5}
	must(t, old.SaveSupply(sp, bobID))
	must(t, old.AdjustSupply(Adjustment{SupplyID: sp.ID, Kind: "used", Amount: 1, UserID: bobID}))
	must(t, old.RequestSupply(sp.ID, bobID, "more please"))
	prob := &Problem{PlaceID: b.ID, Title: "Leak", ReportedBy: annID}
	must(t, old.CreateProblem(prob))
	_, err = old.CreateAPIToken("scanner", annID)
	must(t, err)
	// Raw SQL where today's code needs the new columns.
	for _, q := range []string{
		`UPDATE maintenance_logs SET created_by = ?`,
		`UPDATE problems SET assigned_to = ?`,
	} {
		if _, err := old.DB.Exec(q, bobID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := old.DB.Exec(`INSERT INTO problem_updates (problem_id, note, created_by) VALUES (?, 'on it', ?)`, prob.ID, bobID); err != nil {
		t.Fatal(err)
	}
	token := RandomToken()
	if _, err := old.DB.Exec(`INSERT INTO sessions (token, user_id, csrf_token, expires_at) VALUES (?, ?, 'x', ?)`,
		token, annID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	refs := map[string]int64{
		`SELECT created_by FROM maintenance_logs`: bobID,
		`SELECT created_by FROM supply_changes`:   bobID,
		`SELECT requested_by FROM supplies`:       bobID,
		`SELECT reported_by FROM problems`:        annID,
		`SELECT assigned_to FROM problems`:        bobID,
		`SELECT created_by FROM problem_updates`:  bobID,
		`SELECT created_by FROM api_tokens`:       annID,
	}
	checkRefs := func(st *Store, when string) {
		t.Helper()
		for q, want := range refs {
			rows, err := st.DB.Query(q)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for rows.Next() {
				var got sql.NullInt64
				rows.Scan(&got)
				n++
				if !got.Valid || got.Int64 != want {
					t.Errorf("%s %s: got %v, want %d", when, q, got, want)
				}
			}
			rows.Close()
			if n == 0 {
				t.Errorf("%s %s: no rows", when, q)
			}
		}
	}
	checkRefs(old, "before migration")
	old.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	checkRefs(st, "after migration")
	if got, err := st.GetSession(token); err != nil || got.User.ID != annID || got.Grant != "" {
		t.Errorf("session after migration: %+v %v", got, err)
	}
	u, ok := st.Authenticate("ann", "a long password")
	if !ok || u.ID != annID || u.SSOSubject != "" || !u.Active || u.DisplayName != "Ann A" || !u.IsAdmin {
		t.Errorf("user after migration: %+v %v", u, ok)
	}
	if p, _ := st.GetProblem(prob.ID); p.AssignedTo != bobID || p.AssignedName != "bob" || p.ReportedBy != annID {
		t.Errorf("problem after migration: %+v", p)
	}
	if got, _ := st.GetSupply(sp.ID); got.RequestedBy != "bob" {
		t.Errorf("supply after migration: %+v", got)
	}
	var fk int
	st.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Error("foreign keys should be back on")
	}
	// Deleting a user still sets references to NULL and cascades sessions
	// (the references point at the new table).
	must(t, st.DeleteUser(annID))
	if p, _ := st.GetProblem(prob.ID); p.ReportedBy != 0 {
		t.Errorf("set null after migration: %+v", p)
	}
	if _, err := st.GetSession(token); err != ErrNotFound {
		t.Errorf("cascade after migration: %v", err)
	}
}

func TestUpsertSSOUser(t *testing.T) {
	st := openTest(t)
	localID, _ := st.CreateUser("Tyler", "", "a long password", true)

	// The existing local user is linked by username, case-insensitively.
	u, err := st.UpsertSSOUser("sub-1", "tyler", "Tyler G", false)
	if err != nil || u.ID != localID || u.SSOSubject != "sub-1" || u.Username != "tyler" || u.DisplayName != "Tyler G" || u.IsAdmin || !u.Active {
		t.Fatalf("link: %+v %v", u, err)
	}
	// Found by subject from now on, even after a rename upstream.
	u, _ = st.UpsertSSOUser("sub-1", "tgoza", "Tyler G", true)
	if u.ID != localID || u.Username != "tgoza" || !u.IsAdmin {
		t.Errorf("rename: %+v", u)
	}
	if got, err := st.GetUserBySubject("sub-1"); err != nil || got.ID != localID {
		t.Errorf("by subject: %+v %v", got, err)
	}
	if _, err := st.GetUserBySubject("nope"); err != ErrNotFound {
		t.Errorf("unknown subject: %v", err)
	}

	// A linked user is never taken over by a different subject with the
	// same username: the new person gets a new row and the old one is
	// moved aside.
	other, err := st.UpsertSSOUser("sub-2", "TGOZA", "Someone else", false)
	if err != nil || other.ID == localID || other.SSOSubject != "sub-2" {
		t.Fatalf("collision: %+v %v", other, err)
	}
	old, _ := st.GetUser(localID)
	if old.SSOSubject != "sub-1" || old.Username == "tgoza" {
		t.Errorf("old row should keep its subject and lose the name: %+v", old)
	}
	// Its next sign-in, under a new name, puts it right.
	if u, _ = st.UpsertSSOUser("sub-1", "tyler", "Tyler G", true); u.ID != localID || u.Username != "tyler" {
		t.Errorf("after collision: %+v", u)
	}

	// SSO users have no password, so no local login.
	if _, ok := st.Authenticate("TGOZA", ""); ok {
		t.Error("an SSO-only user signed in with no password")
	}
	if st.HasPassword(other.ID) || !st.HasPassword(localID) {
		t.Error("HasPassword")
	}
	if _, ok := st.Authenticate("tyler", "a long password"); !ok {
		t.Error("the linked user keeps their local password")
	}
	if _, err := st.UpsertSSOUser("", "x", "", false); err == nil {
		t.Error("a subject is required")
	}
}

func TestSyncSSOUsers(t *testing.T) {
	st := openTest(t)
	local, _ := st.CreateUser("local", "", "a long password", false)
	ann, _ := st.UpsertSSOUser("a", "ann", "", false)
	bob, _ := st.UpsertSSOUser("b", "bob", "", false)
	b := building(t, st, "Main")
	prob := &Problem{PlaceID: b.ID, Title: "Leak"}
	must(t, st.CreateProblem(prob))
	change := func(assignee int64) error {
		_, err := st.UpdateProblem(ProblemChange{ProblemID: prob.ID, Status: ProblemOpen, AssignedTo: assignee, UserID: local})
		return err
	}
	must(t, change(bob.ID))
	bobSess, _ := st.CreateSSOSession(bob.ID, "grant-b", time.Hour)

	// Bob's access is removed, Cat is new, Dan is listed but suspended.
	err := st.SyncSSOUsers([]SSOUser{
		{Subject: "a", Username: "ann", DisplayName: "Ann", Active: true},
		{Subject: "c", Username: "cat", Active: true, IsAdmin: true},
		{Subject: "d", Username: "dan", Active: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	users, _ := st.ListUsers()
	active := map[string]bool{}
	for _, u := range users {
		active[u.Username] = u.Active
	}
	want := map[string]bool{"local": true, "ann": true, "bob": false, "cat": true, "dan": false}
	if len(users) != len(want) {
		t.Fatalf("users: %+v", users)
	}
	for name, a := range want {
		if active[name] != a {
			t.Errorf("%s active = %v, want %v", name, active[name], a)
		}
	}
	if _, err := st.GetSession(bobSess.Token); err != ErrNotFound {
		t.Errorf("bob should be signed out: %v", err)
	}
	if _, ok := st.Authenticate("local", "a long password"); !ok {
		t.Error("unlinked local users are left alone")
	}
	if n, _ := st.CountAdmins(); n != 1 {
		t.Errorf("active admins = %d, want 1 (cat)", n)
	}

	// Pickers offer active users, plus inactive ones already attached.
	p, _ := st.GetProblem(prob.ID)
	if p.AssignedTo != bob.ID || p.AssignedName != "bob" {
		t.Errorf("bob should still be assigned: %+v", p)
	}
	pick, _ := st.ListPickableUsers([]int64{p.AssignedTo})
	var names []string
	for _, u := range pick {
		names = append(names, u.Username)
	}
	if got := strings.Join(names, ","); got != "ann,bob,cat,local" {
		t.Errorf("pickable: %s", got)
	}
	if pick, _ = st.ListPickableUsers(nil); len(pick) != 3 {
		t.Errorf("pickable with nothing attached: %+v", pick)
	}

	// Bob stays assigned through other changes, but inactive Dan can't
	// be newly assigned, nor Bob once unassigned.
	if _, err := st.UpdateProblem(ProblemChange{ProblemID: prob.ID, Status: ProblemInProgress, AssignedTo: bob.ID, UserID: local}); err != nil {
		t.Errorf("keeping an inactive assignee: %v", err)
	}
	dan, _ := st.GetUserBySubject("d")
	if err := change(dan.ID); err != ErrNotFound {
		t.Errorf("assigning inactive dan: %v", err)
	}
	must(t, change(ann.ID))
	if err := change(bob.ID); err != ErrNotFound {
		t.Errorf("an inactive user can't be assigned again: %v", err)
	}

	// Back on the list: active again, history intact.
	st.SyncSSOUsers([]SSOUser{{Subject: "b", Username: "bob", Active: true}})
	if u, _ := st.GetUser(bob.ID); !u.Active {
		t.Error("bob should be active again")
	}
	if u, _ := st.GetUser(ann.ID); u.Active {
		t.Error("ann was left off the list")
	}
	must(t, change(bob.ID))
}

func TestSetActive(t *testing.T) {
	st := openTest(t)
	id, _ := st.CreateUser("admin", "", "a long password", true)
	sess, _ := st.CreateSession(id, time.Hour)
	must(t, st.SetActive(id, false))
	if _, ok := st.Authenticate("admin", "a long password"); ok {
		t.Error("an inactive user signed in")
	}
	if _, err := st.GetSession(sess.Token); err != ErrNotFound {
		t.Errorf("an inactive user's session should count as gone: %v", err)
	}
	if n, _ := st.CountAdmins(); n != 0 {
		t.Errorf("inactive admins counted: %d", n)
	}
	must(t, st.SetActive(id, true))
	if _, ok := st.Authenticate("admin", "a long password"); !ok {
		t.Error("reactivated user can't sign in")
	}
}

func TestSSOSessions(t *testing.T) {
	st := openTest(t)
	u, _ := st.UpsertSSOUser("a", "ann", "", false)
	if _, err := st.CreateSSOSession(u.ID, "", time.Hour); err == nil {
		t.Error("an SSO session needs a grant")
	}
	sess, err := st.CreateSSOSession(u.ID, "grant-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession(sess.Token)
	if err != nil || got.Grant != "grant-1" || got.GrantCheckedAt.IsZero() || got.User.SSOSubject != "a" {
		t.Fatalf("session: %+v %v", got, err)
	}
	st.DB.Exec(`UPDATE sessions SET grant_checked_at = '2000-01-01T00:00:00Z'`)
	if err := st.MarkGrantChecked(sess.Token); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetSession(sess.Token); time.Since(got.GrantCheckedAt) > time.Minute {
		t.Errorf("checked at: %v", got.GrantCheckedAt)
	}
	st.CreateSession(u.ID, time.Hour)
	if err := st.DeleteSessionsForUser(u.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	if n != 0 {
		t.Errorf("%d sessions left", n)
	}
}
