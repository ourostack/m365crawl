package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
)

// putProfile writes the record a Teams account keeps of itself: store profiles, key 8:orgid:<user>.
func putProfile(t *testing.T, s *Store, tenant, user string, fields map[string]string) {
	t.Helper()
	raw, _ := json.Marshal(fields)
	key, _ := json.Marshal("8:orgid:" + user)
	if _, err := s.db.Exec(`insert into records(source, tenant_id, user_id, database, store, key_json, value_json, content_hash, first_seen_at, updated_at)
	  values('fixture', ?, ?, 'Teams:profiles:react-web-client:'||?||':'||?||':en-us', 'profiles', ?, ?, 'h', '2026-11-02T00:00:00.000Z', '2026-11-02T00:00:00.000Z')`,
		tenant, user, tenant, user, string(key), string(raw)); err != nil {
		t.Fatal(err)
	}
}

// putIdentity is what a good read of an Outlook profile left behind.
func putIdentity(t *testing.T, s *Store, account, addr string) {
	t.Helper()
	if _, err := s.db.Exec(`insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, outlookIdentityKey+account, addr); err != nil {
		t.Fatal(err)
	}
}

func linkRow(t *testing.T, s *Store, account string) (principal, method string, active, held bool) {
	t.Helper()
	var ended *string
	err := s.db.QueryRow(`select principal_id, method, unlinked_at from calendar_account_links where source='outlook' and account_id=?`, account).Scan(&principal, &method, &ended)
	return principal, method, ended == nil, err == nil
}

func autoLink(t *testing.T, s *Store) AutoLinkResult {
	t.Helper()
	r, err := s.AutoLinkOutlook(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAutoLinkByAddress(t *testing.T) {
	s := newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "Fixture.User@Example.com", "mail": "fixture.user@example.com"})
	putIdentity(t, s, "outlook/Main", "fixture.user@example.com")
	if r := autoLink(t, s); r.Linked != 1 || r.Unlinked != 0 {
		t.Fatalf("%+v", r)
	}
	if p, m, active, _ := linkRow(t, s, "outlook/Main"); p != "t1/u1" || m != OutlookLinkAddress || !active {
		t.Fatalf("%s %s %v", p, m, active)
	}
	if r := autoLink(t, s); r.Linked != 0 || r.Unlinked != 0 {
		t.Fatalf("a link that holds was written again: %+v", r)
	}
	// The method is what `calendar sources` shows.
	if got, err := s.CalendarSources(context.Background(), CalendarSourcesFilter{Now: base, ReadInterval: time.Minute}); err != nil {
		t.Fatal(err)
	} else {
		var link string
		for _, r := range got.Rows {
			if r.AccountID == "outlook/Main" {
				link = r.Link
			}
		}
		if link != OutlookLinkAddress {
			t.Fatalf("calendar sources shows %q", link)
		}
	}
}

func TestAutoLinkIsCaseInsensitiveAndUsesAnyOwnAddress(t *testing.T) {
	s := newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "upn.name@example.com", "mail": "Mail.Name@EXAMPLE.com", "email": " Other@example.com "})
	putIdentity(t, s, "outlook/Main", "MAIL.NAME@example.COM")
	if r := autoLink(t, s); r.Linked != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestAutoLinkNeedsAMatch(t *testing.T) {
	s := newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	addTeamsAccount(t, s, "t2/u2")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "a@example.com"})
	putProfile(t, s, "t2", "u2", map[string]string{})
	for _, addr := range []string{"b@example.com", "", "example.com", "a@"} {
		putIdentity(t, s, "outlook/Main", addr)
		if r := autoLink(t, s); r.Linked != 0 {
			t.Fatalf("%q linked: %+v", addr, r)
		}
		if _, _, _, held := linkRow(t, s, "outlook/Main"); held {
			t.Fatalf("%q wrote a row", addr)
		}
	}
}

func TestAutoLinkRefusesWhatIsNotOneToOne(t *testing.T) {
	s := newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	addTeamsAccount(t, s, "t2/u2")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "same@example.com"})
	putProfile(t, s, "t2", "u2", map[string]string{"mail": "SAME@example.com"})
	putIdentity(t, s, "outlook/Main", "same@example.com")
	if r := autoLink(t, s); r.Linked != 0 {
		t.Fatalf("two Teams accounts with one address linked: %+v", r)
	}

	// Two Outlook profiles with one address, and two profiles that match one Teams account.
	s = newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "a@example.com", "mail": "b@example.com"})
	putIdentity(t, s, "outlook/One", "a@example.com")
	putIdentity(t, s, "outlook/Two", "a@example.com")
	putIdentity(t, s, "outlook/Three", "b@example.com")
	putIdentity(t, s, "outlook/Four", "b@example.com")
	if r := autoLink(t, s); r.Linked != 0 {
		t.Fatalf("an address held by two profiles linked: %+v", r)
	}
	s = newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "a@example.com", "mail": "b@example.com"})
	putIdentity(t, s, "outlook/One", "a@example.com")
	putIdentity(t, s, "outlook/Two", "b@example.com")
	if r := autoLink(t, s); r.Linked != 0 {
		t.Fatalf("two profiles for one Teams account linked: %+v", r)
	}
}

func TestAutoLinkNeverReplacesTheOperator(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	addTeamsAccount(t, s, "t2/u2")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "a@example.com"})
	putProfile(t, s, "t2", "u2", map[string]string{"userPrincipalName": "b@example.com"})
	putIdentity(t, s, "outlook/Main", "a@example.com")

	// An explicit link to another account stays.
	if err := s.SetOutlookLink(ctx, "outlook/Main", "t2/u2", base); err != nil {
		t.Fatal(err)
	}
	autoLink(t, s)
	if p, m, active, _ := linkRow(t, s, "outlook/Main"); p != "t2/u2" || m != OutlookLinkMethod || !active {
		t.Fatalf("explicit link replaced: %s %s %v", p, m, active)
	}
	// An explicit none stays unlinked, with and without a row written before.
	if err := s.SetOutlookLink(ctx, "outlook/Main", "", base); err != nil {
		t.Fatal(err)
	}
	autoLink(t, s)
	if _, m, active, _ := linkRow(t, s, "outlook/Main"); m != OutlookLinkMethod || active {
		t.Fatalf("none was replaced: %s %v", m, active)
	}
	putIdentity(t, s, "outlook/Other", "b@example.com")
	if err := s.SetOutlookLink(ctx, "outlook/Other", "", base); err != nil {
		t.Fatal(err)
	}
	autoLink(t, s)
	if _, _, active, held := linkRow(t, s, "outlook/Other"); !held || active {
		t.Fatal("none on an account that never had a link was lost")
	}
}

func TestExplicitSayingOwnsAnAutomaticLink(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "a@example.com"})
	putIdentity(t, s, "outlook/Main", "a@example.com")
	autoLink(t, s)
	if inEffect(t, s, "outlook/Main", "t1/u1") || inEffect(t, s, "", "t1/u1") {
		t.Fatal("an automatic link is not the operator's")
	}
	if err := s.SetOutlookLink(ctx, "outlook/Main", "t1/u1", base); err != nil {
		t.Fatal(err)
	}
	if _, m, active, _ := linkRow(t, s, "outlook/Main"); m != OutlookLinkMethod || !active {
		t.Fatalf("%s %v", m, active)
	}
	if !inEffect(t, s, "outlook/Main", "t1/u1") || !inEffect(t, s, "", "t1/u1") {
		t.Fatal("the operator's link is not seen")
	}
	// Ending an automatic link makes the ended row the operator's, so it is not made again.
	s = newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "a@example.com"})
	putIdentity(t, s, "outlook/Main", "a@example.com")
	autoLink(t, s)
	if err := s.SetOutlookLink(ctx, "outlook/Main", "", base); err != nil {
		t.Fatal(err)
	}
	if r := autoLink(t, s); r.Linked != 0 {
		t.Fatalf("an ended link was made again: %+v", r)
	}
	if !inEffect(t, s, "", "") {
		t.Fatal("none for every profile is not in effect")
	}
}

func TestAutoLinkFollowsTheEvidence(t *testing.T) {
	s := newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	addTeamsAccount(t, s, "t2/u2")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "a@example.com"})
	putProfile(t, s, "t2", "u2", map[string]string{"userPrincipalName": "b@example.com"})
	putIdentity(t, s, "outlook/Main", "a@example.com")
	autoLink(t, s)
	// The profile now holds another address: the old link ends and the new one is made.
	putIdentity(t, s, "outlook/Main", "b@example.com")
	if r := autoLink(t, s); r.Unlinked != 1 || r.Linked != 1 {
		t.Fatalf("%+v", r)
	}
	if p, m, active, _ := linkRow(t, s, "outlook/Main"); p != "t2/u2" || m != OutlookLinkAddress || !active {
		t.Fatalf("%s %s %v", p, m, active)
	}
	// An address nobody has ends it, and a later match makes it again.
	putIdentity(t, s, "outlook/Main", "")
	if r := autoLink(t, s); r.Unlinked != 1 || r.Linked != 0 {
		t.Fatalf("%+v", r)
	}
	putIdentity(t, s, "outlook/Main", "a@example.com")
	if r := autoLink(t, s); r.Linked != 1 {
		t.Fatalf("%+v", r)
	}
}

// The core refuses a second Outlook account for one Teams account; the automatic link leaves it.
func TestAutoLinkLeavesWhatTheCoreRefuses(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "a@example.com"})
	if err := s.SetOutlookLink(ctx, "outlook/Operator", "t1/u1", base); err != nil {
		t.Fatal(err)
	}
	putIdentity(t, s, "outlook/Main", "a@example.com")
	if r := autoLink(t, s); r.Linked != 0 {
		t.Fatalf("%+v", r)
	}
	if _, _, _, held := linkRow(t, s, "outlook/Main"); held {
		t.Fatal("a refused link left a row")
	}
}

func TestTeamsAddressesSkipWhatIsNotAProfile(t *testing.T) {
	s := newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	if _, err := s.db.Exec(`insert into records(source, tenant_id, user_id, database, store, key_json, value_json, content_hash, first_seen_at, updated_at)
	  values('fixture', 't1', 'u1', 'd', 'profiles', '"8:orgid:u1"', 'not json', 'h', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	putIdentity(t, s, "outlook/Main", "a@example.com")
	if r := autoLink(t, s); r.Linked != 0 {
		t.Fatalf("%+v", r)
	}
	// A record of another person's profile (a key that is not the account's own) holds no identity.
	s = newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	putProfile(t, s, "t1", "someone-else", map[string]string{"userPrincipalName": "a@example.com"})
	if _, err := s.db.Exec(`update records set user_id='u1'`); err != nil {
		t.Fatal(err)
	}
	putIdentity(t, s, "outlook/Main", "a@example.com")
	if r := autoLink(t, s); r.Linked != 0 {
		t.Fatalf("%+v", r)
	}
	// A removed record holds none either.
	s = newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "a@example.com"})
	if _, err := s.db.Exec(`update records set removed_at='2026-11-03T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	putIdentity(t, s, "outlook/Main", "a@example.com")
	if r := autoLink(t, s); r.Linked != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestAutoLinkOnABrokenArchive(t *testing.T) {
	for _, drop := range []string{"records", "meta", "calendar_account_links"} {
		s := newStore(t)
		if _, err := s.db.Exec(`drop table ` + drop); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AutoLinkOutlook(context.Background(), base); err == nil {
			t.Fatalf("dropping %s was not an error", drop)
		}
	}
}

func TestCommitOutlookKeepsTheAddress(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	b := OutlookBatch{Account: "outlook/Main", Events: []calendar.Event{outlookEvent(calendar.TriFalse, base)}, FreshAt: base, At: base, Zone: time.UTC, Stamp: OutlookStamp(3, time.UTC), Addresses: []string{"Fixture@Example.com", "a@example.com", "A@example.com", " "}}
	st, err := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main")
	if err != nil || st.IdentityRead {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := s.CommitOutlook(ctx, b, outlookRun); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := s.db.QueryRow(`select value from meta where key='outlook_identity:outlook/Main'`).Scan(&got); err != nil || got != "a@example.com\nfixture@example.com" {
		t.Fatalf("%q %v", got, err)
	}
	if st, err = s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main"); err != nil || !st.IdentityRead {
		t.Fatalf("%+v %v", st, err)
	}
	b.Addresses = nil
	b.At = b.At.Add(time.Hour)
	if _, err := s.CommitOutlook(ctx, b, outlookRun); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`select value from meta where key='outlook_identity:outlook/Main'`).Scan(&got); err != nil || got != "" {
		t.Fatalf("a read that found no address must forget the old one: %q %v", got, err)
	}
}

// A profile can hold more than one account. It links when exactly one Teams account has one of its
// addresses, and not when its accounts name two Teams accounts.
func TestAutoLinkWithSeveralAccountsInTheProfile(t *testing.T) {
	s := newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	addTeamsAccount(t, s, "t2/u2")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "work@example.com"})
	putProfile(t, s, "t2", "u2", map[string]string{"userPrincipalName": "other@example.com"})
	putIdentity(t, s, "outlook/Main", "personal@example.net\nwork@example.com")
	if r := autoLink(t, s); r.Linked != 1 {
		t.Fatalf("%+v", r)
	}
	if p, _, active, _ := linkRow(t, s, "outlook/Main"); p != "t1/u1" || !active {
		t.Fatalf("%s %v", p, active)
	}
	putIdentity(t, s, "outlook/Main", "other@example.com\nwork@example.com")
	if r := autoLink(t, s); r.Unlinked != 1 || r.Linked != 0 {
		t.Fatalf("a profile that names two Teams accounts must end its automatic link: %+v", r)
	}
	// An address that another profile holds too is not evidence for either.
	putIdentity(t, s, "outlook/Main", "work@example.com\nshared@example.net")
	putIdentity(t, s, "outlook/Second", "shared@example.net")
	if r := autoLink(t, s); r.Linked != 1 {
		t.Fatalf("the other address still links: %+v", r)
	}
	putIdentity(t, s, "outlook/Main", "work@example.com")
	putIdentity(t, s, "outlook/Second", "work@example.com")
	if r := autoLink(t, s); r.Unlinked != 1 || r.Linked != 0 {
		t.Fatalf("an address two profiles hold is no evidence: %+v", r)
	}
}

// A database that refuses the writes of a link is an error of the step and not a silent miss.
func TestAutoLinkWriteFailures(t *testing.T) {
	s := newStore(t)
	addTeamsAccount(t, s, "t1/u1")
	addTeamsAccount(t, s, "t2/u2")
	putProfile(t, s, "t1", "u1", map[string]string{"userPrincipalName": "a@example.com"})
	putProfile(t, s, "t2", "u2", map[string]string{"userPrincipalName": "b@example.com"})
	putIdentity(t, s, "outlook/Main", "a@example.com")
	if _, err := s.db.Exec(`create trigger no_insert before insert on calendar_account_links begin select raise(abort, 'no'); end`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AutoLinkOutlook(context.Background(), base); err == nil {
		t.Fatal("a refused insert was not an error")
	}
	if _, err := s.db.Exec(`drop trigger no_insert`); err != nil {
		t.Fatal(err)
	}
	autoLink(t, s)
	if _, err := s.db.Exec(`create trigger no_update before update on calendar_account_links begin select raise(abort, 'no'); end`); err != nil {
		t.Fatal(err)
	}
	putIdentity(t, s, "outlook/Main", "b@example.com")
	if _, err := s.AutoLinkOutlook(context.Background(), base); err == nil {
		t.Fatal("a refused unlink was not an error")
	}
}
