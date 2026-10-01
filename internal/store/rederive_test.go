package store

import (
	"context"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// seedAlpha1 stores rows the way alpha.1 left them: derived fields blank where this build derives
// text, and no derivation version.
func seedAlpha1(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	call := msg(acctA, "19:c@thread.v2", "1", "", base)
	call.SenderName, call.ContentHTML, call.MessageType = "", "<ended/>", "Event/Call"
	call.Raw = []byte(`{"id":"1","messageType":"Event/Call","content":"<ended/>","fromDisplayNameInToken":"Ana Token"}`)
	broken := msg(acctA, "19:c@thread.v2", "2", "kept", base)
	broken.Raw = []byte(`not json`)
	noRaw := msg(acctA, "19:c@thread.v2", "3", "kept too", base)
	noRaw.Raw = nil
	current := msg(acctA, "19:c@thread.v2", "4", "already right", base)
	current.Raw = []byte(`{"id":"4","messageType":"RichText/Html","content":"<p>already right</p>","imDisplayName":"Sender 4"}`)
	if _, err := s.ApplyMessages(ctx, []teamsdesktop.Message{call, broken, noRaw, current}); err != nil {
		t.Fatal(err)
	}
	group := conv(acctA, "19:g@thread.v2", "Chat", "")
	group.Title, group.Raw = "", []byte(`{"id":"19:g@thread.v2","type":"Chat","members":[{"id":"8:orgid:x","friendlyName":"Ana"},{"id":"8:orgid:y","friendlyName":"Ben"}]}`)
	brokenConv := conv(acctA, "19:b@thread.v2", "Chat", "")
	brokenConv.Raw = []byte(`not json`)
	noRawConv := conv(acctA, "19:n@thread.v2", "Chat", "Named")
	noRawConv.Raw = nil
	fine := conv(acctA, "19:ok@thread.v2", "Chat", "Fine")
	fine.Kind, fine.Raw = "Chat", []byte(`{"id":"19:ok@thread.v2","type":"Chat","chatTitle":{"shortTitle":"Fine"}}`)
	if _, err := s.ApplyConversations(ctx, []teamsdesktop.Conversation{group, brokenConv, noRawConv, fine}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `delete from meta`); err != nil {
		t.Fatal(err)
	}
}

func TestRederiveUpgradesAnAlpha1Archive(t *testing.T) {
	s := newStore(t)
	seedAlpha1(t, s)
	ctx := context.Background()
	m, err := s.Rederive(ctx)
	if err != nil || m == nil || m.From != 1 || m.To != DerivationVersion || m.Rows != 2 {
		t.Fatalf("Rederive = %+v, %v (want the call event and the group chat)", m, err)
	}
	var text, sender, hash string
	if err := s.db.QueryRowContext(ctx, `select content_text, sender_name, content_hash from messages where id='1'`).Scan(&text, &sender, &hash); err != nil {
		t.Fatal(err)
	}
	if text != "Call ended" || sender != "Ana Token" || hash == "" {
		t.Errorf("call row: %q %q %q", text, sender, hash)
	}
	// The index follows the text, and a row that cannot be derived keeps what it had.
	var n int
	if err := s.db.QueryRowContext(ctx, `select count(*) from message_fts where message_fts match 'Call'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("fts finds %d rows for the new text (%v)", n, err)
	}
	for id, want := range map[string]string{"2": "kept", "3": "kept too", "4": "already right"} {
		if err := s.db.QueryRowContext(ctx, `select content_text from messages where id=?`, id).Scan(&text); err != nil || text != want {
			t.Errorf("message %s: %q (%v)", id, text, err)
		}
	}
	// The sender's name reaches the people table.
	if err := s.db.QueryRowContext(ctx, `select display_name from people where id='8:orgid:other-1'`).Scan(&text); err != nil || text != "Ana Token" {
		t.Errorf("people name: %q (%v)", text, err)
	}
	// The untitled chat gets its member-list name, in the row and in the title index.
	var name string
	if err := s.db.QueryRowContext(ctx, `select display_name from conversations where id='19:g@thread.v2'`).Scan(&name); err != nil || name != "Ana, Ben" {
		t.Errorf("group name: %q (%v)", name, err)
	}
	if err := s.db.QueryRowContext(ctx, `select count(*) from conversation_fts where conversation_fts match 'Ben'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("title index finds %d (%v)", n, err)
	}
	// The upgrade is recorded: the next call does nothing, and ApplyMessages sees equal hashes.
	if m, err := s.Rederive(ctx); err != nil || m != nil {
		t.Errorf("second Rederive = %+v, %v", m, err)
	}
	var v string
	if err := s.db.QueryRowContext(ctx, `select value from meta where key='derivation_version'`).Scan(&v); err != nil || v != "2" {
		t.Errorf("stored version %q (%v)", v, err)
	}
	call := msg(acctA, "19:c@thread.v2", "1", "Call ended", base)
	call.SenderName, call.ContentHTML, call.MessageType = "Ana Token", "<ended/>", "Event/Call"
	call.Raw = []byte(`{"id":"1","messageType":"Event/Call","content":"<ended/>","fromDisplayNameInToken":"Ana Token"}`)
	if c, ch, err := s.ApplyMessagesChanges(ctx, []teamsdesktop.Message{call}); err != nil || c.Updated != 0 || len(ch) != 0 {
		t.Errorf("the migrated row reads as changed: %+v %v %v", c, ch, err)
	}
}

func TestRederiveSkipsNewAndCurrentArchives(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if m, err := s.Rederive(ctx); err != nil || m != nil {
		t.Errorf("new archive: %+v, %v", m, err)
	}
	var v string
	if err := s.db.QueryRowContext(ctx, `select value from meta where key='derivation_version'`).Scan(&v); err != nil || v != "2" {
		t.Errorf("a new archive should be stamped current: %q (%v)", v, err)
	}
	// A newer archive (from a later build) is left alone.
	if _, err := s.db.ExecContext(ctx, `update meta set value='99'`); err != nil {
		t.Fatal(err)
	}
	seedAlpha1Rows := func() {
		seedAlpha1(t, s)
		_, _ = s.db.ExecContext(ctx, `insert into meta values('derivation_version','99')`)
	}
	seedAlpha1Rows()
	if m, err := s.Rederive(ctx); err != nil || m != nil {
		t.Errorf("newer archive: %+v, %v", m, err)
	}
}

func TestRederiveRejectsACorruptVersion(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Rederive(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `update meta set value='two'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rederive(ctx); err == nil || !strings.Contains(err.Error(), "derivation_version") {
		t.Errorf("err = %v", err)
	}
}

func TestRederivePagesThroughManyRows(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	var ms []teamsdesktop.Message
	for i := 0; i < rederiveBatch*2+7; i++ {
		id := strings.Repeat("m", 1) + string(rune('a'+i%26)) + strings.Repeat("x", i/26)
		m := msg(acctA, "19:c@thread.v2", id, "", base)
		m.ContentHTML, m.MessageType = "<ended/>", "Event/Call"
		m.Raw = []byte(`{"id":"` + id + `","messageType":"Event/Call","content":"<ended/>"}`)
		ms = append(ms, m)
	}
	if _, err := s.ApplyMessages(ctx, ms); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `delete from meta`); err != nil {
		t.Fatal(err)
	}
	m, err := s.Rederive(ctx)
	if err != nil || m == nil || m.Rows != len(ms) {
		t.Fatalf("Rederive = %+v, %v; want %d rows", m, err, len(ms))
	}
}

func TestRederiveFaults(t *testing.T) {
	sweepFaults(t, func(t *testing.T, s *Store) { seedAlpha1(t, s) }, func(ctx context.Context, s *Store) error {
		_, err := s.Rederive(ctx)
		return err
	})
}
