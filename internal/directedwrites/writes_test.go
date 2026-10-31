package directedwrites

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	da "github.com/ourostack/m365crawl/internal/directedactions"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/workiq"
)

const (
	notesNewest = "/chats/48:notes/messages?$top=1&$orderby=createdDateTime%20desc"
	notesList   = "/chats/48:notes/messages?$top=50&$orderby=createdDateTime%20desc"
	meJSON      = `{"id":"u","mail":"Me@Example.invalid","userPrincipalName":"me.upn@example.invalid"}`
	selfAddr    = "me@example.invalid"
)

type reply struct {
	data   string
	status int
	next   string
	err    error
}

// server is the synthetic Microsoft 365 every fake session shares.
type server struct {
	t        *testing.T
	pages    map[string]reply
	me       map[string]string // /me per --account pin; "" is unpinned
	answer   workiq.WriteAnswer
	created  []string
	onFetch  func(path string)
	opened   []string
	openErr  map[string]error
	closeErr error
	fetched  []string
}

type fake struct{ s *server }

func (f fake) Fetch(_ context.Context, path string) (workiq.Page, error) {
	f.s.fetched = append(f.s.fetched, path)
	if f.s.onFetch != nil {
		f.s.onFetch(path)
	}
	r, ok := f.s.pages[path]
	if path == "/me" {
		pin := f.s.opened[len(f.s.opened)-1]
		data, found := f.s.me[pin]
		if !found {
			data = f.s.me[strings.ToLower(pin)]
		}
		r, ok = reply{data: data}, true
		if r.data == "" {
			r = reply{status: 401, err: errs.Usage("no user")}
		}
	}
	if !ok {
		return workiq.Page{StatusCode: 404}, errs.Usage("not found")
	}
	if r.err != nil {
		return workiq.Page{StatusCode: r.status}, r.err
	}
	return workiq.Page{Data: json.RawMessage(r.data), StatusCode: 200, NextCursor: r.next}, nil
}

func (f fake) CreateChatMessage(_ context.Context, chat, text string) workiq.WriteAnswer {
	f.s.created = append(f.s.created, chat+"|"+text)
	return f.s.answer
}

func (f fake) CreateMailDraft(_ context.Context, subject, text string, to []string) workiq.WriteAnswer {
	f.s.created = append(f.s.created, "draft|"+subject+"|"+text+"|"+strings.Join(to, ","))
	return f.s.answer
}

func (f fake) Close() error { return f.s.closeErr }

func message(id, at, from, text string) string {
	raw, _ := json.Marshal(map[string]any{"id": id, "chatId": "48:notes", "createdDateTime": at,
		"from": map[string]any{"user": map[string]any{"id": from}}, "body": map[string]any{"contentType": "text", "content": text}})
	return string(raw)
}

func list(messages ...string) string { return `{"value":[` + strings.Join(messages, ",") + `]}` }

func newServer(t *testing.T, enabled, account bool) (*server, Options) {
	t.Helper()
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "m365crawl.db")
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		if err := s.SetWorkIQEnabled(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	if account {
		if err := s.ApplyAccount(ctx, teamsdesktop.Account{TenantID: "t", UserID: "u"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	srv := &server{t: t, me: map[string]string{"": meJSON, selfAddr: meJSON, "me.upn@example.invalid": meJSON, "other@example.invalid": meJSON},
		pages: map[string]reply{
			notesNewest:                     {data: list(message("100", "2026-10-31T10:00:00Z", "u", "old"))},
			"/chats/48:notes/messages/1700": {data: message("1700", "2026-10-31T11:00:00Z", "u", "hello there")},
		},
		answer:  workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 201, ServerID: "1700", ChatID: "48:notes", FromUserID: "u"},
		openErr: map[string]error{}}
	oldOpen, oldPause, oldNow, oldRead, oldWrite := openSession, savePause, now, readArchive, writeArchive
	openSession = func(_ context.Context, _ workiq.Invocation, account string) (session, error) {
		srv.opened = append(srv.opened, account)
		if err := srv.openErr[account]; err != nil {
			return nil, err
		}
		return fake{srv}, nil
	}
	savePause = 0
	t.Cleanup(func() {
		openSession, savePause, now, readArchive, writeArchive = oldOpen, oldPause, oldNow, oldRead, oldWrite
	})
	return srv, Options{DBPath: db, Version: "test"}
}

func turnOn(t *testing.T, opts Options) {
	t.Helper()
	if _, err := SetSwitch(opts.DBPath, true); err != nil {
		t.Fatal(err)
	}
}

func notesRequest(text string) da.Request {
	return da.Request{Contract: da.Contract, Action: SendTeamsMessage, Agent: "agent", Reason: "asked",
		Identity: "ME@example.invalid", Target: da.Target{Conversation: "48:notes", Recipients: []string{"me.upn@example.invalid"}},
		Content: da.Content{Text: text}}
}

func draftRequest() da.Request {
	return da.Request{Contract: da.Contract, Action: SaveMailDraft, Agent: "agent", Reason: "asked",
		Target: da.Target{Recipients: []string{"Friend@Example.invalid"}}, Content: da.Content{Text: "body\r\ntext", Subject: "Plan"}}
}

func logged(t *testing.T, opts Options) []store.DirectedWrite {
	t.Helper()
	var rows []store.DirectedWrite
	if err := readOnly(context.Background(), opts.DBPath, func(s *store.Store) (err error) {
		rows, err = s.DirectedWrites(context.Background(), 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return rows
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var c *errs.Coded
	if !errors.As(err, &c) || c.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestSwitchFailsClosedAndRoundTrips(t *testing.T) {
	db := filepath.Join(t.TempDir(), "a.db")
	if ReadSwitch(db).On {
		t.Fatal("a missing switch is on")
	}
	for _, raw := range []string{`{`, `{"writes":"yes"}`, `{"writes":"on","extra":1}`, `[]`} {
		if err := os.WriteFile(SwitchPath(db), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if ReadSwitch(db).On {
			t.Fatal("malformed switch is on:", raw)
		}
	}
	at := time.Date(2026, 10, 31, 12, 0, 0, 5, time.UTC)
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
	for _, on := range []bool{true, false} {
		sw, err := SetSwitch(db, on)
		if err != nil || sw.On != on || !sw.ChangedAt.Equal(at) {
			t.Fatal(sw, err)
		}
		if got := ReadSwitch(db); got.On != on || !got.ChangedAt.Equal(at) {
			t.Fatal("read back", got)
		}
		info, err := os.Stat(SwitchPath(db))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("switch file is not private", info, err)
		}
	}
	if _, err := SetSwitch(filepath.Join(t.TempDir(), "missing", "a.db"), true); err == nil {
		t.Fatal("created a missing archive directory")
	}
	blocked := filepath.Join(t.TempDir(), "b.db")
	if err := os.MkdirAll(filepath.Join(SwitchPath(blocked), "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := SetSwitch(blocked, true); err == nil {
		t.Fatal("replaced a directory")
	}
	if _, err := os.Stat(SwitchPath(blocked) + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("left its temporary file", err)
	}
}

func TestSendRefusesOfflineWithoutStartingWorkIQ(t *testing.T) {
	srv, opts := newServer(t, true, true)
	ctx := context.Background()
	got, err := Send(ctx, opts, notesRequest("hello"))
	if err != nil || got.Code != da.CodeWritesOff || got.ExitClass != da.ExitPersonNeeded || !got.TellPerson {
		t.Fatal(got, err)
	}
	turnOn(t, opts)
	for _, tc := range []struct {
		edit  func(*da.Request)
		code  da.Code
		field string
	}{
		{func(r *da.Request) { r.Agent = " " }, da.CodeMissingAgent, ""},
		{func(r *da.Request) { r.Identity = "" }, da.CodeIdentityRequired, ""},
		{func(r *da.Request) { r.Target.Recipients = nil }, da.CodeInvalidRequest, "target.recipients"},
		{func(r *da.Request) { r.Action, r.Target, r.IntentKey = SaveMailDraft, da.Target{}, "k" }, da.CodeIntentKeyNotAllowed, ""},
		{func(r *da.Request) { r.Content.Text = "a\u202eb" }, da.CodeInvalidRequest, "content.text"},
		{func(r *da.Request) { r.Content.Text = "a\x00b" }, da.CodeInvalidRequest, "content.text"},
		{func(r *da.Request) { r.Content.Text = "\xff" }, da.CodeInvalidRequest, "content.text"},
		{func(r *da.Request) { r.Content.Text = strings.Repeat("a", maxText+1) }, da.CodeInvalidRequest, "content.text"},
		{func(r *da.Request) { r.Action, r.Target.Conversation, r.Content.Subject = SaveMailDraft, "", "a\nb" }, da.CodeInvalidRequest, "content.subject"},
		{func(r *da.Request) { r.Target.Recipients = []string{"a\tb@example.invalid"} }, da.CodeInvalidRequest, "target.recipients"},
	} {
		req := notesRequest("hello")
		tc.edit(&req)
		got, err := Send(ctx, opts, req)
		if err != nil || got.Decision != da.Refuse || got.Code != tc.code || got.Field != tc.field || got.ExitClass == "" {
			t.Fatalf("%+v %v want %s %s", got, err, tc.code, tc.field)
		}
	}
	if len(srv.opened) != 0 {
		t.Fatal("WorkIQ started for an offline refusal", srv.opened)
	}
	for _, text := range []string{"tab\there", "emoji \u200d joined", "line\nbreak", "\r\nwindows\r\n"} {
		if field, ok := checkInput(notesRequest(text)); !ok {
			t.Fatal("refused allowed text", text, field)
		}
	}
	for _, r := range []rune{0x7f, 0x85, 0x2066, 0xe0001, 0xfeff, 0x200b, 0x2060, 0xad, 0x2028, 0x2029} {
		if _, ok := checkInput(notesRequest("a" + string(r) + "b")); ok {
			t.Fatalf("allowed %U", r)
		}
	}
}

func TestSendNeedsWorkIQEnabled(t *testing.T) {
	_, opts := newServer(t, false, true)
	turnOn(t, opts)
	_, err := Send(context.Background(), opts, notesRequest("hello"))
	requireCode(t, err, "workiq_disabled")
	missing := Options{DBPath: filepath.Join(t.TempDir(), "none.db")}
	if _, err := SetSwitch(missing.DBPath, true); err != nil {
		t.Fatal(err)
	}
	_, err = Send(context.Background(), missing, notesRequest("hello"))
	requireCode(t, err, "workiq_disabled")
	readArchive = func(context.Context, string, func(*store.Store) error) error { return errors.New("broken") }
	if _, err := Send(context.Background(), opts, notesRequest("hello")); err == nil || err.Error() != "broken" {
		t.Fatal(err)
	}
}

func TestSendToNotesToSelfIsVerifiedThenReplayed(t *testing.T) {
	srv, opts := newServer(t, true, true)
	turnOn(t, opts)
	ctx := context.Background()
	req := notesRequest("  hello\r\nthere ")
	req.IntentKey, req.ExpectedVersion = "k1", "100"
	got, err := Send(ctx, opts, req)
	want := Result{Contract: da.Contract, Action: SendTeamsMessage, Decision: da.Accept, Effect: da.VisibleToOthers, RequestID: "1", Outcome: da.Sent,
		Phase: "verified", Verification: "found", ServerID: "1700", ExitClass: da.ExitDone}
	if err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}
	if !slices.Equal(srv.opened, []string{"ME@example.invalid"}) || !slices.Equal(srv.created, []string{"48:notes|hello\nthere"}) {
		t.Fatal(srv.opened, srv.created)
	}
	rows := logged(t, opts)
	if len(rows) != 1 || rows[0].Identity != selfAddr || rows[0].Recipients[0] != selfAddr || rows[0].UserID != "u" || rows[0].TenantID != "t" ||
		rows[0].AccountPin != "ME@example.invalid" || rows[0].WatermarkID != "100" || rows[0].WatermarkAt != "2026-10-31T10:00:00Z" ||
		rows[0].Outcome != "sent" || rows[0].Text != "hello\nthere" || rows[0].ExpectedVersion != "100" || rows[0].Version != "test" {
		t.Fatalf("%+v", rows)
	}
	req.Reason = "asked again"
	got, err = Send(ctx, opts, req)
	if err != nil || got.Decision != da.Replay || got.ReplayOf != "1" || got.EarlierOutcome != da.Sent || got.ExitClass != da.ExitDone || len(srv.created) != 1 {
		t.Fatalf("replay %+v %v", got, err)
	}
	other := notesRequest("different")
	other.IntentKey = "k1"
	if got, _ := Send(ctx, opts, other); got.Code != da.CodeIntentKeyReused || len(srv.created) != 1 {
		t.Fatalf("reuse %+v", got)
	}
	stale := notesRequest("new")
	stale.ExpectedVersion = "99"
	if got, _ := Send(ctx, opts, stale); got.Code != da.CodeStaleVersion {
		t.Fatalf("stale %+v", got)
	}
	wrong := notesRequest("new")
	wrong.Target.Recipients = []string{"someone@example.invalid"}
	if got, _ := Send(ctx, opts, wrong); got.Code != da.CodeRecipientsChanged {
		t.Fatalf("audience %+v", got)
	}
	mismatch := notesRequest("new")
	mismatch.Identity = "other@example.invalid"
	if got, _ := Send(ctx, opts, mismatch); got.Code != da.CodeIdentityMismatch {
		t.Fatalf("identity %+v", got)
	}
}

func TestSendOutcomes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		answer    workiq.WriteAnswer
		readBack  *reply
		outcome   da.Outcome
		phase     string
		server    string
		class     da.ExitClass
		errorCode string
	}{
		{"rejected", workiq.WriteAnswer{Kind: workiq.WriteRejected, Status: 400, Code: "BadRequest"}, nil, da.Failed, "answered", "", da.ExitFailed, "BadRequest"},
		{"lost", workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_session_lost"}, nil, da.Unknown, "answered", "", da.ExitUnknown, "workiq_session_lost"},
		{"elsewhere", workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 201, ServerID: "1700", ChatID: "19:x", FromUserID: "u"}, nil, da.Unknown, "answered", "1700", da.ExitUnknown, ""},
		{"other sender", workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 201, ServerID: "1700", ChatID: "48:notes", FromUserID: "x"}, nil, da.Unknown, "answered", "1700", da.ExitUnknown, ""},
		{"read back differs", workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 201, ServerID: "1700", ChatID: "48:notes", FromUserID: "u"},
			&reply{data: message("1700", "2026-10-31T11:00:00Z", "u", "edited")}, da.Unknown, "answered", "1700", da.ExitUnknown, ""},
		{"read back fails", workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 201, ServerID: "1700", ChatID: "48:notes", FromUserID: "u"},
			&reply{status: 503, err: errs.Usage("x")}, da.Unknown, "answered", "1700", da.ExitUnknown, ""},
		{"read back undecodable", workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 201, ServerID: "1700", ChatID: "48:notes", FromUserID: "u"},
			&reply{data: `{"id":""}`}, da.Unknown, "answered", "1700", da.ExitUnknown, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, opts := newServer(t, true, true)
			turnOn(t, opts)
			srv.answer = tc.answer
			if tc.readBack != nil {
				srv.pages["/chats/48:notes/messages/1700"] = *tc.readBack
			}
			got, err := Send(ctx, opts, notesRequest("hello there"))
			if err != nil || got.Outcome != tc.outcome || got.Phase != tc.phase || got.ServerID != tc.server || got.ExitClass != tc.class ||
				got.TellPerson != (tc.outcome == da.Unknown) {
				t.Fatalf("%+v %v", got, err)
			}
			row := logged(t, opts)[0]
			if row.Outcome != string(tc.outcome) || row.ErrorCode != tc.errorCode || row.ServerID != tc.server {
				t.Fatalf("%+v", row)
			}
			again, _ := Send(ctx, opts, notesRequest("hello there"))
			if tc.outcome == da.Unknown && (again.Code != da.CodeOutcomeUnknown || !again.TellPerson || len(srv.created) != 1) {
				t.Fatalf("an unknown write was resent: %+v", again)
			}
		})
	}
}

func TestSendToAGroupChatChecksItsWholeAudience(t *testing.T) {
	srv, opts := newServer(t, true, true)
	turnOn(t, opts)
	ctx := context.Background()
	chat := "19:group@thread.v2"
	srv.pages["/chats/"+chat+"?$select=id,chatType"] = reply{data: `{"id":"19:group@thread.v2","chatType":"group"}`}
	srv.pages["/chats/"+chat+"/members"] = reply{data: `{"value":[{"userId":"u","email":"ME.UPN@example.invalid"},{"userId":"v","email":"Friend@Example.invalid"},{"userId":"W-GUEST","email":""}]}`}
	srv.pages["/chats/"+chat+"/messages?$top=1&$orderby=createdDateTime%20desc"] = reply{data: `{"value":[]}`}
	srv.pages["/chats/"+chat+"/messages/1700"] = reply{data: strings.Replace(message("1700", "2026-10-31T11:00:00Z", "u", "hi"), "48:notes", chat, 1)}
	srv.answer.ChatID = chat
	req := notesRequest("hi")
	req.Target = da.Target{Conversation: chat, Recipients: []string{"me@example.invalid", "friend@example.invalid", "w-guest"}}
	got, err := Send(ctx, opts, req)
	if err != nil || got.Outcome != da.Sent {
		t.Fatalf("%+v %v", got, err)
	}
	if row := logged(t, opts)[0]; row.WatermarkID != "" || row.WatermarkAt != "" {
		t.Fatalf("an empty chat has a watermark: %+v", row)
	}
	target, err := Target(ctx, opts, chat, "")
	if err != nil || !target.Known || target.Identity != selfAddr || target.Version != "none" || target.WatermarkAt != "" ||
		!slices.Equal(target.Recipients, []string{selfAddr, "friend@example.invalid", "w-guest"}) {
		t.Fatalf("%+v %v", target, err)
	}
	req.Target.Recipients = req.Target.Recipients[:2]
	if got, _ := Send(ctx, opts, req); got.Code != da.CodeRecipientsChanged {
		t.Fatalf("%+v", got)
	}
}

func TestSendRefusesAChatWhoseAudienceIsNotEstablished(t *testing.T) {
	ctx := context.Background()
	chat := "19:a_b@unq.gbl.spaces"
	info, members, newestPath := "/chats/"+chat+"?$select=id,chatType", "/chats/"+chat+"/members", "/chats/"+chat+"/messages?$top=1&$orderby=createdDateTime%20desc"
	two := `{"value":[{"userId":"u","email":"me@example.invalid"},{"userId":"v","email":"v@example.invalid"}]}`
	for _, tc := range []struct {
		name  string
		pages map[string]reply
	}{
		{"chat gone", map[string]reply{}},
		{"members gone", map[string]reply{info: {data: `{"chatType":"group"}`}}},
		{"members paged", map[string]reply{info: {data: `{"chatType":"group"}`}, members: {data: two, next: "/chats/x/members?$skiptoken=2"}}},
		{"members unreadable", map[string]reply{info: {data: `{"chatType":"group"}`}, members: {data: `{"value":"x"}`}}},
		{"only me listed", map[string]reply{info: {data: `{"chatType":"oneOnOne"}`}, members: {data: `{"value":[{"userId":"u","email":"me@example.invalid"}]}`}}},
		{"me not listed", map[string]reply{info: {data: `{"chatType":"group"}`}, members: {data: `{"value":[{"userId":"v"},{"userId":"x"}]}`}}},
		{"one on one with three", map[string]reply{info: {data: `{"chatType":"oneOnOne"}`}, members: {data: `{"value":[{"userId":"u"},{"userId":"v"},{"userId":"x"}]}`}}},
		{"messages gone", map[string]reply{info: {data: `{"chatType":"group"}`}, members: {data: two}}},
		{"messages unreadable", map[string]reply{info: {data: `{"chatType":"group"}`}, members: {data: two}, newestPath: {data: `{"value":1}`}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, opts := newServer(t, true, true)
			turnOn(t, opts)
			for k, v := range tc.pages {
				srv.pages[k] = v
			}
			req := notesRequest("hi")
			req.Target = da.Target{Conversation: chat, Recipients: []string{selfAddr, "v@example.invalid"}}
			got, err := Send(ctx, opts, req)
			if err != nil || got.Code != da.CodeUnknownTarget || len(srv.created) != 0 {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	srv, opts := newServer(t, true, true)
	turnOn(t, opts)
	req := notesRequest("hi")
	req.Target.Conversation = "48:saved"
	if got, err := Send(ctx, opts, req); err != nil || got.Code != da.CodeUnknownTarget || len(srv.fetched) != 1 {
		t.Fatalf("%+v %v %v", got, err, srv.fetched)
	}
}

func TestSendStopsWhenTheServerCannotBeRead(t *testing.T) {
	ctx := context.Background()
	chat := "19:a@thread.v2"
	info, members, newestPath := "/chats/"+chat+"?$select=id,chatType", "/chats/"+chat+"/members", "/chats/"+chat+"/messages?$top=1&$orderby=createdDateTime%20desc"
	two := `{"value":[{"userId":"u"},{"userId":"v"}]}`
	busy := reply{status: 503, err: errs.Usage("busy")}
	for _, pages := range []map[string]reply{
		{info: busy},
		{info: {data: `{}`}, members: busy},
		{info: {data: `{}`}, members: {data: two}, newestPath: busy},
		{info: {data: `{}`}, members: {data: two}, newestPath: {data: `{"value":[{"id":""}]}`}},
	} {
		srv, opts := newServer(t, true, true)
		turnOn(t, opts)
		for k, v := range pages {
			srv.pages[k] = v
		}
		req := notesRequest("hi")
		req.Target = da.Target{Conversation: chat, Recipients: []string{selfAddr, "u2"}}
		if _, err := Send(ctx, opts, req); err == nil || len(srv.created) != 0 {
			t.Fatal("sent without reading the target", pages)
		}
		if _, err := Target(ctx, opts, chat, selfAddr); err == nil {
			t.Fatal("target read without the server")
		}
	}
}

func TestSendNeedsAnArchivedTeamsAccountAndAWorkingWorkIQ(t *testing.T) {
	ctx := context.Background()
	_, opts := newServer(t, true, false)
	turnOn(t, opts)
	_, err := Send(ctx, opts, notesRequest("hi"))
	requireCode(t, err, "workiq_chat_account_unmatched")

	srv, opts := newServer(t, true, true)
	turnOn(t, opts)
	srv.openErr["ME@example.invalid"] = errs.Usage("not installed")
	if _, err := Send(ctx, opts, notesRequest("hi")); err == nil {
		t.Fatal("sent without WorkIQ")
	}
	srv.openErr = map[string]error{}
	srv.me["ME@example.invalid"] = ""
	if _, err := Send(ctx, opts, notesRequest("hi")); err == nil {
		t.Fatal("sent without /me")
	}
	srv.me["ME@example.invalid"] = `{"id":"u","mail":"not an address"}`
	if _, err := Send(ctx, opts, notesRequest("hi")); err == nil {
		t.Fatal("sent as an unusable address")
	}
	srv.me["ME@example.invalid"] = meJSON
	calls := 0
	readArchive = func(ctx context.Context, path string, fn func(*store.Store) error) error {
		calls++
		if calls == 3 {
			return errors.New("broken log")
		}
		return readOnly(ctx, path, fn)
	}
	if _, err := Send(ctx, opts, notesRequest("hi")); err == nil || err.Error() != "broken log" {
		t.Fatal(err)
	}
	calls = 0
	readArchive = func(ctx context.Context, path string, fn func(*store.Store) error) error {
		calls++
		if calls == 2 {
			return errors.New("broken accounts")
		}
		return readOnly(ctx, path, fn)
	}
	if _, err := Send(ctx, opts, notesRequest("hi")); err == nil || err.Error() != "broken accounts" {
		t.Fatal(err)
	}
	if len(srv.created) != 0 {
		t.Fatal("sent", srv.created)
	}
}

func TestDraftWithoutIdentityPinsTheSignedInUser(t *testing.T) {
	srv, opts := newServer(t, true, false)
	turnOn(t, opts)
	ctx := context.Background()
	srv.answer = workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 201, ServerID: "draft/1", IsDraft: true}
	srv.pages["/me/messages/draft%2F1?$select=id,isDraft,subject,body,toRecipients"] = reply{data: `{"id":"draft/1","isDraft":true,"subject":"Plan",
		"body":{"contentType":"html","content":"<p>body</p><p>text</p>"},"toRecipients":[{"emailAddress":{"address":"friend@example.invalid"}}]}`}
	got, err := Send(ctx, opts, draftRequest())
	if err != nil || got.Outcome != da.Sent || got.Effect != da.OnlyYou || got.ServerID != "draft/1" {
		t.Fatalf("%+v %v", got, err)
	}
	if !slices.Equal(srv.opened, []string{"", selfAddr}) || !slices.Equal(srv.created, []string{"draft|Plan|body\ntext|friend@example.invalid"}) {
		t.Fatal(srv.opened, srv.created)
	}
	if row := logged(t, opts)[0]; row.Identity != selfAddr || row.AccountPin != selfAddr || row.Conversation != "" {
		t.Fatalf("%+v", row)
	}
	srv.answer.IsDraft = false
	if got, _ := Send(ctx, opts, draftRequest()); got.Outcome != da.Unknown {
		t.Fatalf("a sent mail counted as a draft: %+v", got)
	}
}

func TestDraftPinningFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		setup func(*server)
		code  string
	}{
		{"first open", func(s *server) { s.openErr[""] = errs.Usage("x") }, "usage"},
		{"first me", func(s *server) { s.me[""] = "" }, "usage"},
		{"close", func(s *server) { s.closeErr = errs.Usage("close") }, "usage"},
		{"no address", func(s *server) { s.me[""] = `{"id":"u"}` }, "workiq_account_invalid"},
		{"reopen", func(s *server) { s.openErr[selfAddr] = errs.Usage("x") }, "usage"},
		{"another user", func(s *server) { s.me[selfAddr] = `{"id":"z","mail":"me@example.invalid"}` }, "workiq_identity_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, opts := newServer(t, true, false)
			turnOn(t, opts)
			tc.setup(srv)
			_, err := Send(ctx, opts, draftRequest())
			requireCode(t, err, tc.code)
			if len(srv.created) != 0 {
				t.Fatal("created", srv.created)
			}
		})
	}
}

func TestTheWriteAheadDecisionSeesChangesMadeMeanwhile(t *testing.T) {
	ctx := context.Background()
	srv, opts := newServer(t, true, true)
	turnOn(t, opts)
	srv.onFetch = func(path string) {
		if path == notesNewest {
			if _, err := SetSwitch(opts.DBPath, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got, err := Send(ctx, opts, notesRequest("hi")); err != nil || got.Code != da.CodeWritesOff || len(srv.created) != 0 {
		t.Fatalf("sent after writes were turned off: %+v %v", got, err)
	}
	srv, opts = newServer(t, true, true)
	turnOn(t, opts)
	req := notesRequest("hi")
	req.IntentKey = "k"
	srv.onFetch = func(path string) {
		if path != notesNewest {
			return
		}
		srv.onFetch = nil
		earlier := store.DirectedWrite{Contract: da.Contract, Action: SendTeamsMessage, Effect: "visible_to_others", Agent: "a", Reason: "r",
			Identity: selfAddr, Conversation: "48:notes", Recipients: []string{selfAddr}, Text: "hi", IntentKey: "k", UserID: "u", Outcome: "sent", Phase: "verified"}
		if err := store.WithWorkIQWriter(ctx, opts.DBPath, func(ctx context.Context, s *store.Store) error {
			_, err := s.BeginDirectedWrite(ctx, earlier, func([]store.DirectedWrite) error { return nil })
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := Send(ctx, opts, req); err != nil || got.Decision != da.Replay || got.ReplayOf != "1" || len(srv.created) != 0 {
		t.Fatalf("a concurrent identical send went out twice: %+v %v", got, err)
	}
	srv, opts = newServer(t, true, true)
	turnOn(t, opts)
	writeArchive = func(context.Context, string, func(context.Context, *store.Store) error) error {
		return errs.Usage("locked")
	}
	if _, err := Send(ctx, opts, notesRequest("hi")); err == nil || len(srv.created) != 0 {
		t.Fatal("sent without a write-ahead record", err)
	}
}

func TestAnAnswerThatCannotBeSavedIsStillPrinted(t *testing.T) {
	ctx := context.Background()
	_, opts := newServer(t, true, true)
	turnOn(t, opts)
	calls := 0
	writeArchive = func(ctx context.Context, path string, fn func(context.Context, *store.Store) error) error {
		calls++
		if calls > 1 {
			return errors.New("busy")
		}
		return store.WithWorkIQWriter(ctx, path, fn)
	}
	got, err := Send(ctx, opts, notesRequest("hello there"))
	if err != nil || got.Outcome != da.Sent || !strings.Contains(got.Notice, "writes verify 1") || calls != 1+saveAttempts {
		t.Fatalf("%+v %v %d", got, err, calls)
	}
	if row := logged(t, opts)[0]; row.Outcome != "unknown" || row.Phase != "in_flight" {
		t.Fatalf("%+v", row)
	}
}
