package directedwrites

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	da "github.com/ourostack/m365crawl/internal/directedactions"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

const (
	originalID = "<orig@example.invalid>"
	replyID    = "<reply@example.invalid>"
	draftPage  = "/me/messages/draft-1?$select=id,isDraft,toRecipients,ccRecipients,bccRecipients,internetMessageId,body"
	draftState = "/me/messages/draft-1?$select=id,isDraft"
	sentFolder = "/me/mailFolders/sentitems?$select=id"
)

var (
	originalLookup = byInternetMessageID("", originalID, "id,parentFolderId,changeKey,isDraft,from,replyTo,subject")
	sentLookup     = byInternetMessageID("sentitems", replyID, "id,internetMessageId,toRecipients,ccRecipients,bccRecipients")
)

const (
	sentCopy     = `{"id":"sentcopy","parentFolderId":"sent-folder","changeKey":"ck-s","isDraft":false,"from":{"emailAddress":{"address":"me@example.invalid"}},"subject":"Plan"}`
	receivedCopy = `{"id":"inbox-copy","parentFolderId":"inbox","changeKey":"ck-1","isDraft":false,"from":{"emailAddress":{"address":"ME.UPN@example.invalid"}},"subject":"Plan"}`
	goodDraft    = `{"id":"draft-1","isDraft":true,"internetMessageId":"<reply@example.invalid>","toRecipients":[{"emailAddress":{"address":"me@example.invalid"}}],
	"body":{"contentType":"html","content":"<html><head><style>p{}</style></head><body>hello &amp; you<br>there<hr style=\"x\">quoted original</body></html>"}}`
)

// mailServer is a world with one mail the person sent to themselves, a received and a sent copy.
func mailServer(t *testing.T) (*server, Options) {
	t.Helper()
	srv, opts := newServer(t, true, false)
	srv.pages[originalLookup] = reply{data: list(sentCopy, receivedCopy)}
	srv.pages[sentFolder] = reply{data: `{"id":"sent-folder"}`}
	srv.pages[draftPage] = reply{data: goodDraft}
	srv.pages[draftState] = reply{data: `{"id":"draft-1","isDraft":true}`}
	srv.pages[sentLookup] = reply{data: list(`{"id":"sent-1","toRecipients":[{"emailAddress":{"address":"Me@Example.invalid"}}]}`)}
	srv.replyAnswer = workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 201, ServerID: "draft-1", IsDraft: true, InternetMessageID: replyID}
	srv.sendAnswer = workiq.WriteAnswer{Kind: workiq.WriteAccepted, Status: 202}
	oldAttempts, oldPause := refindAttempts, refindPause
	refindAttempts, refindPause = 2, 0
	t.Cleanup(func() { refindAttempts, refindPause = oldAttempts, oldPause })
	turnOn(t, opts)
	return srv, opts
}

func replyRequest() da.Request {
	return da.Request{Contract: da.Contract, Action: ReplyMail, Agent: "agent", Reason: "asked", Identity: selfAddr,
		Target: da.Target{Message: originalID, Recipients: []string{"me.upn@example.invalid"}}, Content: da.Content{Text: "hello & you\nthere"}}
}

func draftOfWrite(t *testing.T, opts Options, id int64) (string, bool) {
	t.Helper()
	draft, _, ok, err := draftOf(context.Background(), opts, id)
	if err != nil {
		t.Fatal(err)
	}
	return draft, ok
}

func TestReplyIsSentThroughACheckedDraftAndFoundInSentItems(t *testing.T) {
	srv, opts := mailServer(t)
	ctx := context.Background()
	req := replyRequest()
	req.IntentKey, req.ExpectedVersion = "k", "ck-1"
	got, err := Send(ctx, opts, req)
	if err != nil || got.Decision != da.Accept || got.Outcome != da.Sent || got.Verification != "found" || got.ServerID != "sent-1" ||
		got.ExitClass != da.ExitDone || got.TellPerson || got.Notice != "" {
		t.Fatalf("%+v %v", got, err)
	}
	if !slices.Equal(srv.created, []string{"reply|inbox-copy|hello &amp; you<br>there", "send|draft-1"}) {
		t.Fatal(srv.created)
	}
	if draft, ok := draftOfWrite(t, opts, 1); !ok || draft != "draft-1" {
		t.Fatal("the draft was not recorded", draft)
	}
	row := logged(t, opts)[0]
	if row.Message != originalID || row.Conversation != "" || row.Recipients[0] != selfAddr || row.Outcome != "sent" {
		t.Fatalf("%+v", row)
	}
	req.Reason = "again"
	if again, _ := Send(ctx, opts, req); again.Decision != da.Replay || len(srv.created) != 2 {
		t.Fatalf("%+v", again)
	}
	target, err := Target(ctx, opts, "", " "+originalID+" ", "")
	if err != nil || !target.Known || target.Message != originalID || target.Chat != "" || target.Version != "ck-1" ||
		!slices.Equal(target.Recipients, []string{selfAddr}) {
		t.Fatalf("%+v %v", target, err)
	}
}

func TestReplyAudienceAndVersion(t *testing.T) {
	ctx := context.Background()
	srv, opts := mailServer(t)
	srv.pages[originalLookup] = reply{data: list(strings.Replace(receivedCopy, `"subject"`, `"replyTo":[{"emailAddress":{"address":"List@Example.invalid"}}],"subject"`, 1))}
	if got, _ := Send(ctx, opts, replyRequest()); got.Code != da.CodeRecipientsChanged {
		t.Fatalf("%+v", got)
	}
	target, err := Target(ctx, opts, "", originalID, selfAddr)
	if err != nil || !slices.Equal(target.Recipients, []string{"list@example.invalid"}) {
		t.Fatalf("%+v %v", target, err)
	}
	srv, opts = mailServer(t)
	stale := replyRequest()
	stale.ExpectedVersion = "ck-0"
	if got, _ := Send(ctx, opts, stale); got.Code != da.CodeStaleVersion || len(srv.created) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestReplyRefusesMailItCannotPinDown(t *testing.T) {
	ctx := context.Background()
	other := strings.Replace(receivedCopy, `"id":"inbox-copy"`, `"id":"inbox-copy-2"`, 1)
	for _, tc := range []struct {
		name    string
		message string
		lookup  *reply
		code    da.Code
	}{
		{"not a message id", "orig-without-brackets", nil, da.CodeUnknownTarget},
		{"gone", originalID, &reply{status: 404, err: errs.Usage("x")}, da.CodeUnknownTarget},
		{"paged", originalID, &reply{data: list(receivedCopy), next: "/me/messages?$skiptoken=2"}, da.CodeUnknownTarget},
		{"unreadable", originalID, &reply{data: `{"value":1}`}, da.CodeUnknownTarget},
		{"only in Sent Items", originalID, &reply{data: list(sentCopy)}, da.CodeUnknownTarget},
		{"nothing", originalID, &reply{data: list()}, da.CodeUnknownTarget},
		{"only drafts", originalID, &reply{data: list(strings.Replace(receivedCopy, `"isDraft":false`, `"isDraft":true`, 1))}, da.CodeTargetMismatch},
		{"copies disagree", originalID, &reply{data: list(receivedCopy, strings.Replace(other, `"Plan"`, `"Other"`, 1))}, da.CodeUnknownTarget},
		{"no sender", originalID, &reply{data: list(`{"id":"inbox-copy","parentFolderId":"inbox","subject":"Plan"}`)}, da.CodeUnknownTarget},
		{"blank reply-to", originalID, &reply{data: list(strings.Replace(receivedCopy, `"subject"`, `"replyTo":[{"emailAddress":{"address":" "}}],"subject"`, 1))}, da.CodeUnknownTarget},
		{"unusable id", originalID, &reply{data: list(strings.Replace(receivedCopy, `"inbox-copy"`, `"inbox/copy"`, 1))}, da.CodeUnknownTarget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, opts := mailServer(t)
			if tc.lookup != nil {
				srv.pages[originalLookup] = *tc.lookup
			}
			req := replyRequest()
			req.Target.Message = tc.message
			got, err := Send(ctx, opts, req)
			if err != nil || got.Code != tc.code || len(srv.created) != 0 {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	srv, opts := mailServer(t)
	srv.pages[originalLookup] = reply{data: list(other, receivedCopy)}
	if target, err := Target(ctx, opts, "", originalID, ""); err != nil || !target.Known || len(srv.created) != 0 {
		t.Fatalf("agreeing copies: %+v %v", target, err)
	}
	if got, err := Target(ctx, opts, "", "nope", ""); err != nil || got.Known || got.Message != "nope" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, folder := range []string{`{}`, `[]`} {
		srv, opts := mailServer(t)
		srv.pages[originalLookup] = reply{data: list(sentCopy)}
		srv.pages[sentFolder] = reply{data: folder}
		if got, err := Send(ctx, opts, replyRequest()); err != nil || got.Code != da.CodeUnknownTarget || len(srv.created) != 0 {
			t.Fatalf("a Sent Items copy became a target without the folder id: %+v %v", got, err)
		}
	}
	for _, broken := range []string{originalLookup, sentFolder} {
		srv, opts := mailServer(t)
		srv.pages[broken] = reply{status: 503, err: errs.Usage("busy")}
		if _, err := Send(ctx, opts, replyRequest()); err == nil || len(srv.created) != 0 {
			t.Fatal("replied without reading the mail", broken)
		}
		if _, err := Target(ctx, opts, "", originalID, ""); err == nil {
			t.Fatal("target read without the server", broken)
		}
	}
}

func TestReplyIsNotSentWhenTheDraftIsWrong(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		draft reply
	}{
		{"unreadable", reply{status: 503, err: errs.Usage("x")}},
		{"not json", reply{data: `[]`}},
		{"another recipient", reply{data: strings.Replace(goodDraft, `"toRecipients":[`, `"ccRecipients":[{"emailAddress":{"address":"x@example.invalid"}}],"toRecipients":[`, 1)}},
		{"not a draft", reply{data: strings.Replace(goodDraft, `"isDraft":true`, `"isDraft":false`, 1)}},
		{"no message id", reply{data: strings.Replace(goodDraft, `"<reply@example.invalid>"`, `""`, 1)}},
		{"other text", reply{data: strings.Replace(goodDraft, "hello &amp; you", "hello", 1)}},
		{"text only in the quote", reply{data: strings.Replace(goodDraft, "hello &amp; you<br>there<hr", "intro<hr", 1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, opts := mailServer(t)
			srv.pages[draftPage] = tc.draft
			got, err := Send(ctx, opts, replyRequest())
			if err != nil || got.Outcome != da.Failed || got.ExitClass != da.ExitFailed || !strings.Contains(got.Notice, "draft-1") ||
				slices.Contains(srv.created, "send|draft-1") {
				t.Fatalf("%+v %v %v", got, err, srv.created)
			}
			if row := logged(t, opts)[0]; row.ErrorCode != "reply_draft_mismatch" {
				t.Fatalf("%+v", row)
			}
		})
	}
	for _, body := range []string{`{"contentType":"text","content":"hello & you\nthere\n> quoted"}`, `{"contentType":"html","content":"<p>hello &amp; you<br>there</p><p>quoted</p>"}`} {
		srv, opts := mailServer(t)
		srv.pages[draftPage] = reply{data: `{"id":"draft-1","isDraft":true,"internetMessageId":"<r@x>","toRecipients":[{"emailAddress":{"address":"me@example.invalid"}}],"body":` + body + `}`}
		if got, err := Send(ctx, opts, replyRequest()); err != nil || got.Outcome != da.Sent || !slices.Contains(srv.created, "send|draft-1") {
			t.Fatalf("a draft without a rule: %+v %v", got, err)
		}
	}
}

func TestReplyDraftThatWasNotMadeIsNotSent(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		answer workiq.WriteAnswer
		code   string
		notice string
	}{
		{workiq.WriteAnswer{Kind: workiq.WriteRejected, Status: 404, Code: "ErrorItemNotFound"}, "ErrorItemNotFound", "may be"},
		{workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_timeout"}, "workiq_timeout", "may be"},
		{workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 201, ServerID: "draft-1", InternetMessageID: replyID}, "reply_draft_unusable", "draft-1"},
		{workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 201, ServerID: "draft/1", IsDraft: true, InternetMessageID: replyID}, "reply_draft_unusable", "draft/1"},
	} {
		srv, opts := mailServer(t)
		srv.replyAnswer = tc.answer
		got, err := Send(ctx, opts, replyRequest())
		if err != nil || got.Outcome != da.Failed || !strings.Contains(got.Notice, tc.notice) || len(srv.created) != 1 {
			t.Fatalf("%+v %v %v", got, err, srv.created)
		}
		if row := logged(t, opts)[0]; row.ErrorCode != tc.code {
			t.Fatalf("%+v", row)
		}
		if _, ok := draftOfWrite(t, opts, 1); ok {
			t.Fatal("recorded a draft that was not made")
		}
	}
}

func TestReplySendAnswers(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		send     workiq.WriteAnswer
		draft    *reply
		sent     *reply
		outcome  da.Outcome
		tell     bool
		serverID string
	}{
		{"refused and still a draft", workiq.WriteAnswer{Kind: workiq.WriteRejected, Status: 400, Code: "ErrorInvalidRecipients"}, nil, nil, da.Failed, false, ""},
		{"refused and the draft is gone", workiq.WriteAnswer{Kind: workiq.WriteRejected, Status: 404, Code: "ErrorItemNotFound"}, &reply{status: 404, err: errs.Usage("gone")}, nil, da.Unknown, true, ""},
		{"refused and the draft was sent", workiq.WriteAnswer{Kind: workiq.WriteRejected, Status: 404, Code: "ErrorItemNotFound"}, &reply{data: `{"isDraft":false}`}, nil, da.Unknown, true, ""},
		{"lost", workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_session_lost"}, nil, nil, da.Unknown, true, ""},
		{"accepted but not in Sent Items", workiq.WriteAnswer{Kind: workiq.WriteAccepted, Status: 202}, nil, &reply{data: list()}, da.Unknown, true, ""},
		{"accepted, Sent Items unreadable", workiq.WriteAnswer{Kind: workiq.WriteAccepted, Status: 202}, nil, &reply{status: 503, err: errs.Usage("x")}, da.Unknown, true, ""},
		{"accepted to other recipients", workiq.WriteAnswer{Kind: workiq.WriteAccepted, Status: 202}, nil,
			&reply{data: list(`{"id":"sent-1","toRecipients":[{"emailAddress":{"address":"x@example.invalid"}}]}`)}, da.Sent, true, "sent-1"},
		{"accepted twice", workiq.WriteAnswer{Kind: workiq.WriteAccepted, Status: 202}, nil,
			&reply{data: list(`{"id":"sent-1","toRecipients":[{"emailAddress":{"address":"me@example.invalid"}}]}`, `{"id":"sent-2"}`)}, da.Sent, true, "sent-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, opts := mailServer(t)
			srv.sendAnswer = tc.send
			if tc.draft != nil {
				srv.pages[draftState] = *tc.draft
			}
			if tc.sent != nil {
				srv.pages[sentLookup] = *tc.sent
			}
			got, err := Send(ctx, opts, replyRequest())
			if err != nil || got.Outcome != tc.outcome || got.TellPerson != tc.tell || got.ServerID != tc.serverID {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestReplyChecksTheLogBeforeEachCall(t *testing.T) {
	ctx := context.Background()
	settle := func(t *testing.T, opts Options) {
		t.Helper()
		if err := store.WithWorkIQWriter(ctx, opts.DBPath, func(ctx context.Context, s *store.Store) error {
			_, err := s.UpdateDirectedWrite(ctx, 1, "unknown", "in_flight", store.DirectedWriteChange{Outcome: "failed", Phase: "settled", Verification: "person", Cause: "person_not_sent"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv, opts := mailServer(t)
	srv.onReply = func() { settle(t, opts) }
	if got, err := Send(ctx, opts, replyRequest()); err != nil || got.Outcome != da.Failed || got.Phase != "settled" ||
		!strings.Contains(got.Notice, "draft-1") || len(srv.created) != 1 {
		t.Fatalf("settled before the draft was recorded: %+v %v %v", got, err, srv.created)
	}
	srv, opts = mailServer(t)
	srv.onReply = func() {
		settle(t, opts)
		readArchive = func(context.Context, string, func(*store.Store) error) error { return errors.New("unreadable") }
	}
	if got, err := Send(ctx, opts, replyRequest()); err != nil || got.RequestID != "1" || got.ExitClass != da.ExitUnknown || len(srv.created) != 1 {
		t.Fatalf("an unreadable log lost the write's id: %+v %v", got, err)
	}
	readArchive = readOnly
	srv, opts = mailServer(t)
	srv.onFetch = func(path string) {
		if path == draftPage {
			settle(t, opts)
		}
	}
	if got, err := Send(ctx, opts, replyRequest()); err != nil || got.Phase != "settled" || slices.Contains(srv.created, "send|draft-1") {
		t.Fatalf("settled before the send: %+v %v %v", got, err, srv.created)
	}
	srv, opts = mailServer(t)
	calls := 0
	readArchive = func(ctx context.Context, path string, fn func(*store.Store) error) error {
		calls++
		if calls == 4 {
			return errors.New("unreadable")
		}
		return readOnly(ctx, path, fn)
	}
	if got, err := Send(ctx, opts, replyRequest()); err != nil || got.Outcome != da.Failed || slices.Contains(srv.created, "send|draft-1") {
		t.Fatalf("%+v %v %v", got, err, srv.created)
	}
	readArchive = readOnly
	srv, opts = mailServer(t)
	writes := 0
	writeArchive = func(ctx context.Context, path string, fn func(context.Context, *store.Store) error) error {
		writes++
		if writes >= 2 {
			return errors.New("busy")
		}
		return store.WithWorkIQWriter(ctx, path, fn)
	}
	if got, err := Send(ctx, opts, replyRequest()); err != nil || got.Outcome != da.Failed || len(srv.created) != 1 ||
		!strings.Contains(got.Notice, "could not be saved") {
		t.Fatalf("sent a draft it could not record: %+v %v %v", got, err, srv.created)
	}
}

func TestVerifyReply(t *testing.T) {
	ctx := context.Background()
	unknownReply := func(t *testing.T, recordDraft bool) (*server, Options) {
		t.Helper()
		srv, opts := mailServer(t)
		srv.sendAnswer = workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_session_lost"}
		srv.pages[sentLookup] = reply{data: list()}
		if !recordDraft {
			srv.replyAnswer = workiq.WriteAnswer{Kind: workiq.WriteCreated, ServerID: "draft-1", IsDraft: true, InternetMessageID: replyID}
			writes := 0
			writeArchive = func(ctx context.Context, path string, fn func(context.Context, *store.Store) error) error {
				writes++
				if writes >= 2 && writes <= 2+saveAttempts {
					return errors.New("busy") // the draft and the answer are never recorded
				}
				return store.WithWorkIQWriter(ctx, path, fn)
			}
		}
		if got, _ := Send(ctx, opts, replyRequest()); recordDraft && got.Outcome != da.Unknown || got.Outcome == da.Sent {
			t.Fatal(got)
		}
		writeArchive = store.WithWorkIQWriter
		return srv, opts
	}
	srv, opts := unknownReply(t, true)
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Unknown {
		t.Fatal("not yet in Sent Items, too soon to prove", got, err)
	}
	srv.pages[sentLookup] = reply{data: list(`{"id":"sent-1","toRecipients":[{"emailAddress":{"address":"me@example.invalid"}}]}`)}
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Sent || got.ServerID != "sent-1" || got.TellPerson {
		t.Fatal(got, err)
	}
	srv, opts = unknownReply(t, true)
	srv.pages[sentLookup] = reply{data: list(`{"id":"sent-1"}`)}
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Sent || !got.TellPerson || got.Notice == "" {
		t.Fatal(got, err)
	}
	_, opts = unknownReply(t, true)
	now = later(11 * time.Minute)
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Failed || got.Verification != "not_sent_proven" {
		t.Fatal(got, err)
	}
	srv, opts = unknownReply(t, true)
	srv.pages[draftState] = reply{status: 404, err: errs.Usage("gone")}
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Unknown {
		t.Fatal("a gone draft proved not sent", got, err)
	}
	_, opts = unknownReply(t, false)
	now = time.Now
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Unknown {
		t.Fatal(got, err)
	}
	now = later(11 * time.Minute)
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Failed || got.Verification != "not_sent_proven" {
		t.Fatal(got, err)
	}
	_, opts = unknownReply(t, true)
	calls := 0
	readArchive = func(ctx context.Context, path string, fn func(*store.Store) error) error {
		calls++
		if calls == 3 {
			return errors.New("broken drafts")
		}
		return readOnly(ctx, path, fn)
	}
	if _, err := Verify(ctx, opts, 1); err == nil || err.Error() != "broken drafts" {
		t.Fatal(err)
	}
}
