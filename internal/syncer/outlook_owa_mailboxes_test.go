package syncer

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookowa"
)

// owaBaseResult is the committed fixture's real mail, reused under per-mailbox native identities.
func owaBaseResult(t *testing.T, root string) outlookowa.Result {
	t.Helper()
	p := owaProfile(root)
	o, err := outlookowa.Open(context.Background(), p.Source.LevelDBDir, p.Source.BlobDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = o.Close() }()
	r, err := outlookowa.Collect(context.Background(), o, "", true)
	if err != nil || len(r.Messages) != 5 {
		t.Fatalf("fixture mail = %d, %v", len(r.Messages), err)
	}
	return r
}

func owaSyntheticMailbox(base outlookowa.Result, prefix, address string) outlookowa.Mailbox {
	id := func(s string) string { return prefix + "/" + s }
	r := base
	r.RootID = id(base.RootID)
	r.Folders = nil
	for _, f := range base.Folders {
		f.ID, f.ParentID = id(f.ID), id(f.ParentID)
		r.Folders = append(r.Folders, f)
	}
	r.Messages = nil
	for _, m := range base.Messages {
		m.ID, m.FolderID = id(m.ID), id(m.FolderID)
		m.AttachmentIDs = slices.Clone(m.AttachmentIDs)
		for i := range m.AttachmentIDs {
			m.AttachmentIDs[i] = id(m.AttachmentIDs[i])
		}
		r.Messages = append(r.Messages, m)
	}
	r.SeenIDs = nil
	for _, s := range base.SeenIDs {
		r.SeenIDs = append(r.SeenIDs, id(s))
	}
	identity := ""
	if address != "" {
		identity = outlookowa.MailboxIdentity(address, "owner@example.invalid")
	}
	return outlookowa.Mailbox{Identity: identity, Result: r}
}

// stubOWAMailboxes makes the snapshot's reader report the given mailboxes.
func stubOWAMailboxes(t *testing.T, mailboxes *[]outlookowa.Mailbox) {
	t.Helper()
	old := outlookOWACollectMailboxes
	t.Cleanup(func() { outlookOWACollectMailboxes = old })
	outlookOWACollectMailboxes = func(context.Context, *indexeddb.Origin) ([]outlookowa.Mailbox, error) {
		return slices.Clone(*mailboxes), nil
	}
}

func mailboxAccount(address string) string {
	return "outlook/Default/mailbox-" + outlookowa.MailboxIdentity(address, "owner@example.invalid")[:16]
}

func detailKeys(t *testing.T, db, account string) []int {
	t.Helper()
	rows, err := openRaw(t, db).Query(`select detail_key from mail_messages where account=? order by detail_key`, account)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []int
	for rows.Next() {
		var k int
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		out = append(out, k)
	}
	return out
}

func accountCount(t *testing.T, db, q, account string) int {
	t.Helper()
	var n int
	if err := openRaw(t, db).QueryRow(q, account).Scan(&n); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return n
}

func TestSyncOWASeveralMailboxesStayAccountIsolated(t *testing.T) {
	isolateTmp(t)
	root, db := owaRoot(t, "calendar"), newDB(t)
	base := owaBaseResult(t, root)
	a, b := owaSyntheticMailbox(base, "A", "owner@example.invalid"), owaSyntheticMailbox(base, "B", "shared@example.invalid")
	current := []outlookowa.Mailbox{a}
	stubOWAMailboxes(t, &current)
	o := outlookOpts(db, root)

	r, _ := run(t, o)
	if m := sourceKeyed(t, r, "outlook|Default|mail"); m.Status != StatusOK || m.Counts.Mail.Added != 5 || m.Accounts[0] != "outlook/Default" {
		t.Fatalf("first mailbox: %+v", m)
	}
	legacyKeys := detailKeys(t, db, "outlook/Default")

	current = []outlookowa.Mailbox{a, b}
	o.FullRead = true
	r, _ = run(t, o)
	second := mailboxAccount("shared@example.invalid")
	secondKey := "outlook|" + strings.TrimPrefix(second, "outlook/") + "|mail"
	if m := sourceKeyed(t, r, secondKey); m.Status != StatusOK || m.Counts.Mail.Added != 5 || len(m.Accounts) != 1 || m.Accounts[0] != second {
		t.Fatalf("second mailbox: %+v", r.Sources)
	}
	if m := sourceKeyed(t, r, "outlook|Default|mail"); m.Status != StatusOK || m.Counts.Mail.Added != 0 {
		t.Fatalf("first mailbox reread: %+v", m)
	}
	if !slices.Equal(detailKeys(t, db, "outlook/Default"), legacyKeys) || len(detailKeys(t, db, second)) != 5 {
		t.Fatal("public IDs moved or the second mailbox was not archived")
	}
	if accountCount(t, db, `select count(*) from mail_source_ids where account=? and source_id like 'B/%'`, "outlook/Default") != 0 ||
		accountCount(t, db, `select count(*) from mail_source_ids where account=? and source_id like 'A/%'`, second) != 0 {
		t.Fatal("native identities crossed accounts")
	}
	for _, acct := range []string{"outlook/Default", second} {
		if accountCount(t, db, `select count(*) from meta where key='outlook_mail_read:'||?`, acct) != 1 {
			t.Fatalf("%s has no independent read marker", acct)
		}
	}

	current = []outlookowa.Mailbox{b, a}
	r, _ = run(t, o)
	if sourceKeyed(t, r, secondKey).Counts.Mail.Added != 0 || sourceKeyed(t, r, "outlook|Default|mail").Counts.Mail.Added != 0 {
		t.Fatal("database order changed attribution")
	}
	o.FullRead = false
	r, _ = run(t, o)
	if sourceKeyed(t, r, secondKey).Status != StatusUnchanged || sourceKeyed(t, r, "outlook|Default|mail").Status != StatusUnchanged {
		t.Fatalf("unchanged profile reread: %+v", r.Sources)
	}

	// A removed mailbox keeps its rows and drops out of the profile's reads.
	current = []outlookowa.Mailbox{a}
	o.FullRead = true
	r, _ = run(t, o)
	for _, s := range r.Sources {
		if s.Source == secondKey && s.Status != StatusUnavailable {
			t.Fatalf("removed mailbox still read: %+v", s)
		}
	}
	if len(detailKeys(t, db, second)) != 5 || count(t, db, `select count(*) from mail_absent`) != 0 {
		t.Fatal("removed mailbox lost rows")
	}
	current = []outlookowa.Mailbox{a, b}
	r, _ = run(t, o)
	if m := sourceKeyed(t, r, secondKey); m.Status != StatusOK || m.Counts.Mail.Added != 0 {
		t.Fatalf("returning mailbox was not reattached by identity: %+v", m)
	}
}

func TestSyncOWALegacyArchiveMigratesByNativeOverlap(t *testing.T) {
	isolateTmp(t)
	root, db := owaRoot(t, "calendar"), newDB(t)
	base := owaBaseResult(t, root)
	legacy := owaSyntheticMailbox(base, "A", "")
	current := []outlookowa.Mailbox{legacy}
	stubOWAMailboxes(t, &current)
	o := outlookOpts(db, root)
	run(t, o)
	keys := detailKeys(t, db, "outlook/Default")
	if count(t, db, `select count(*) from meta where key like 'outlook_mailbox_identity:%'`) != 0 {
		t.Fatal("an unproved identity was bound")
	}
	// The upgraded cache reports identities; the mailbox sharing native IDs keeps the old account.
	current = []outlookowa.Mailbox{owaSyntheticMailbox(base, "B", "shared@example.invalid"), owaSyntheticMailbox(base, "A", "owner@example.invalid")}
	o.FullRead = true
	r, _ := run(t, o)
	if m := sourceKeyed(t, r, "outlook|Default|mail"); m.Status != StatusOK || m.Counts.Mail.Added != 0 || !slices.Equal(detailKeys(t, db, "outlook/Default"), keys) {
		t.Fatalf("legacy mailbox moved: %+v", m)
	}
	if len(detailKeys(t, db, mailboxAccount("shared@example.invalid"))) != 5 {
		t.Fatal("new mailbox not archived separately")
	}
	if accountCount(t, db, `select count(*) from meta where key='outlook_mailbox_identity:'||?`, "outlook/Default") != 1 {
		t.Fatal("migrated mailbox was not bound")
	}
}

func TestSyncOWAUnrelatedSingleMailboxIsNotConflated(t *testing.T) {
	isolateTmp(t)
	root, db := owaRoot(t, "calendar"), newDB(t)
	base := owaBaseResult(t, root)
	current := []outlookowa.Mailbox{owaSyntheticMailbox(base, "A", "owner@example.invalid")}
	stubOWAMailboxes(t, &current)
	o := outlookOpts(db, root)
	run(t, o)
	keys := detailKeys(t, db, "outlook/Default")
	// The profile now holds a different, unrelated mailbox only.
	current = []outlookowa.Mailbox{owaSyntheticMailbox(base, "C", "other@example.invalid")}
	o.FullRead = true
	r, _ := run(t, o)
	other := mailboxAccount("other@example.invalid")
	if m := sourceKeyed(t, r, "outlook|"+strings.TrimPrefix(other, "outlook/")+"|mail"); m.Status != StatusOK || m.Counts.Mail.Added != 5 {
		t.Fatalf("unrelated mailbox: %+v", r.Sources)
	}
	if !slices.Equal(detailKeys(t, db, "outlook/Default"), keys) || count(t, db, `select count(*) from mail_absent`) != 0 {
		t.Fatal("unrelated mailbox changed or absented the first mailbox")
	}
}

func TestSyncOWAAttributionRefusalsApplyNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		before []string // prefix=address of mailboxes committed first; empty address is unproved
		after  []string
		detail string
	}{
		"several_unproved":   {[]string{"A=owner@example.invalid"}, []string{"A=owner@example.invalid", "B="}, "multiple_mailboxes"},
		"duplicate_identity": {[]string{"A=owner@example.invalid"}, []string{"A=owner@example.invalid", "B=owner@example.invalid"}, "mailbox_identity_duplicate"},
		"ambiguous_overlap":  {[]string{"A=owner@example.invalid", "B=shared@example.invalid"}, []string{"AB=renamed@example.invalid"}, "mailbox_binding_ambiguous"},
		"legacy_unrelated":   {[]string{"A="}, []string{"C="}, "mailbox_binding_unproved"},
		"unproved_with_more": {[]string{"A=owner@example.invalid", "B=shared@example.invalid"}, []string{"A="}, "mailbox_identity_unresolved"},
	} {
		t.Run(name, func(t *testing.T) {
			isolateTmp(t)
			root, db := owaRoot(t, "calendar"), newDB(t)
			base := owaBaseResult(t, root)
			build := func(specs []string) []outlookowa.Mailbox {
				var out []outlookowa.Mailbox
				for _, spec := range specs {
					prefix, address, _ := strings.Cut(spec, "=")
					if prefix == "AB" {
						mb := owaSyntheticMailbox(base, "A", address)
						other := owaSyntheticMailbox(base, "B", address)
						mb.Result.Folders = append(mb.Result.Folders, other.Result.Folders...)
						out = append(out, mb)
						continue
					}
					out = append(out, owaSyntheticMailbox(base, prefix, address))
				}
				return out
			}
			current := build(tc.before)
			stubOWAMailboxes(t, &current)
			o := outlookOpts(db, root)
			run(t, o)
			rows := count(t, db, `select count(*) from mail_messages`)
			identities := count(t, db, `select count(*) from meta where key like 'outlook_mailbox_identity:%'`)
			current = build(tc.after)
			o.FullRead = true
			r, _, err := Run(context.Background(), o)
			if err == nil {
				t.Fatal("refusal reported success")
			}
			failed := 0
			for _, s := range r.Sources {
				if strings.HasSuffix(s.Source, "|mail") {
					if s.Status != StatusFailed || s.Error == nil || s.Error.Code != "outlook_mail_layout_unsupported" || !strings.HasSuffix(s.Error.Message, tc.detail) {
						t.Fatalf("mail source %s = %+v", s.Source, s.Error)
					}
					failed++
				}
			}
			if failed != len(tc.before) {
				t.Fatalf("failed sources = %d, want every held account", failed)
			}
			if count(t, db, `select count(*) from mail_messages`) != rows || count(t, db, `select count(*) from mail_absent`) != 0 ||
				count(t, db, `select count(*) from meta where key like 'outlook_mailbox_identity:%'`) != identities ||
				count(t, db, `select count(*) from meta where key like 'outlook_mail_read:%'`) != 0 ||
				count(t, db, `select count(*) from meta where key like 'outlook_mail_failure:%'`) != len(tc.before) {
				t.Fatal("refused attribution applied mail, absence, bindings or freshness")
			}
		})
	}
}
