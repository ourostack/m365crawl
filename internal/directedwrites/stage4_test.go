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
	editDraft     = "/me/messages/edit-1?$select=id,isDraft,subject,toRecipients,ccRecipients,bccRecipients,body"
	editDraftRead = editDraft
	editDraftGone = "/me/messages/edit-1?$select=id"
	editDraftNow  = "/me/messages/edit-1?$select=id,isDraft"
	noteMessage   = "/chats/48:notes/messages/1700"
	oldDraft      = `{"id":"edit-1","isDraft":true,"subject":"Old","body":{"contentType":"text","content":"old"},"@odata.etag":"W/\"ck-d\""}`
	newMailDraft  = `{"id":"draft-1","isDraft":true,"internetMessageId":"<reply@example.invalid>","subject":"Plan",
	"toRecipients":[{"emailAddress":{"address":"friend@example.invalid"}}],"body":{"contentType":"text","content":"hello & you\nthere"}}`
)

func sendMailRequest() da.Request {
	return da.Request{Contract: da.Contract, Action: SendMail, Agent: "agent", Reason: "asked", Identity: selfAddr,
		Target: da.Target{Recipients: []string{"Friend@Example.invalid"}}, Content: da.Content{Subject: "Plan", Text: "hello & you\nthere"}}
}

func TestNewMailGoesThroughACheckedDraft(t *testing.T) {
	ctx := context.Background()
	srv, opts := mailServer(t)
	srv.answer = srv.replyAnswer
	srv.pages[draftPage] = reply{data: newMailDraft}
	srv.pages[sentLookup] = reply{data: list(`{"id":"sent-1","toRecipients":[{"emailAddress":{"address":"friend@example.invalid"}}]}`)}
	req := sendMailRequest()
	req.IntentKey = "m1"
	got, err := Send(ctx, opts, req)
	if err != nil || got.Outcome != da.Sent || got.ServerID != "sent-1" || got.Effect != da.VisibleToOthers {
		t.Fatalf("%+v %v", got, err)
	}
	if !slices.Equal(srv.created, []string{"draft|Plan|hello & you\nthere|friend@example.invalid", "send|draft-1"}) {
		t.Fatal(srv.created)
	}
	if again, _ := Send(ctx, opts, req); again.Decision != da.Replay {
		t.Fatalf("%+v", again)
	}
	for name, draft := range map[string]string{
		"other subject": strings.Replace(newMailDraft, `"Plan"`, `"Other"`, 1),
		"a cc":          strings.Replace(newMailDraft, `"toRecipients"`, `"ccRecipients":[{"emailAddress":{"address":"friend@example.invalid"}}],"toRecipients"`, 1),
		"a bcc":         strings.Replace(newMailDraft, `"toRecipients"`, `"bccRecipients":[{"emailAddress":{"address":"x@example.invalid"}}],"toRecipients"`, 1),
	} {
		srv, opts := mailServer(t)
		srv.answer = srv.replyAnswer
		srv.pages[draftPage] = reply{data: draft}
		if got, err := Send(ctx, opts, sendMailRequest()); err != nil || got.Outcome != da.Failed || slices.Contains(srv.created, "send|draft-1") {
			t.Fatalf("%s: %+v %v", name, got, err)
		}
	}
}

func TestReplyAllReachesTheWholeThread(t *testing.T) {
	ctx := context.Background()
	thread := `{"id":"inbox-copy","parentFolderId":"inbox","changeKey":"ck-1","isDraft":false,"subject":"Plan",
	"from":{"emailAddress":{"address":"lee@example.invalid"}},
	"toRecipients":[{"emailAddress":{"address":"ME.UPN@example.invalid"}},{"emailAddress":{"address":"Sam@Example.invalid"}}],
	"ccRecipients":[{"emailAddress":{"address":"kim@example.invalid"}}]}`
	srv, opts := mailServer(t)
	srv.pages[originalLookup] = reply{data: list(thread)}
	target, err := Target(ctx, opts, TargetQuery{Message: originalID, All: true})
	if err != nil || !target.Known || !target.All || !slices.Equal(target.Recipients, []string{"kim@example.invalid", "lee@example.invalid", "sam@example.invalid"}) {
		t.Fatalf("%+v %v", target, err)
	}
	if one, _ := Target(ctx, opts, TargetQuery{Message: originalID}); !slices.Equal(one.Recipients, []string{"lee@example.invalid"}) {
		t.Fatalf("%+v", one)
	}
	req := replyRequest()
	req.Action, req.Target.Recipients = ReplyAllMail, []string{"lee@example.invalid", "sam@example.invalid", "kim@example.invalid"}
	srv.replyAnswer = workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 201, ServerID: "draft-1", IsDraft: true, InternetMessageID: replyID}
	srv.pages[draftPage] = reply{data: `{"id":"draft-1","isDraft":true,"internetMessageId":"<reply@example.invalid>",
	"toRecipients":[{"emailAddress":{"address":"lee@example.invalid"}},{"emailAddress":{"address":"sam@example.invalid"}}],
	"ccRecipients":[{"emailAddress":{"address":"kim@example.invalid"}}],"body":{"contentType":"html","content":"hello &amp; you<br>there<hr>q"}}`}
	srv.pages[sentLookup] = reply{data: list(`{"id":"sent-1","toRecipients":[{"emailAddress":{"address":"lee@example.invalid"}},{"emailAddress":{"address":"sam@example.invalid"}}],
	"ccRecipients":[{"emailAddress":{"address":"kim@example.invalid"}}]}`)}
	got, err := Send(ctx, opts, req)
	if err != nil || got.Outcome != da.Sent || got.TellPerson || srv.created[0] != "replyall|inbox-copy|hello &amp; you<br>there" {
		t.Fatalf("%+v %v %v", got, err, srv.created)
	}
	short := replyRequest()
	short.Action = ReplyAllMail
	if got, _ := Send(ctx, opts, short); got.Code != da.CodeRecipientsChanged {
		t.Fatalf("%+v", got)
	}
}

func TestAReplyAllIsNotAResendOfAnUnknownReply(t *testing.T) {
	ctx := context.Background()
	srv, opts := mailServer(t)
	srv.sendAnswer = workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_session_lost"}
	if got, _ := Send(ctx, opts, replyRequest()); got.Outcome != da.Unknown {
		t.Fatal(got)
	}
	again := replyRequest()
	again.Action = ReplyAllMail
	if got, _ := Send(ctx, opts, again); got.Code != da.CodeOutcomeUnknown || len(srv.created) != 2 {
		t.Fatalf("a reply-all resent an unknown reply: %+v %v", got, srv.created)
	}
}

func draftServer(t *testing.T) (*server, Options) {
	t.Helper()
	srv, opts := mailServer(t)
	srv.pages[editDraft] = reply{data: oldDraft}
	updated := reply{data: `{"id":"edit-1","isDraft":true,"subject":"New","toRecipients":[{"emailAddress":{"address":"a@example.invalid"}}],
	"body":{"contentType":"html","content":"<p>new text</p>"}}`}
	srv.onAct = func() { srv.pages[editDraftRead] = updated }
	srv.pages[editDraftNow] = reply{data: `{"id":"edit-1","isDraft":true}`}
	srv.actAnswer = workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 200, ServerID: "edit-1", IsDraft: true}
	return srv, opts
}

func updateRequest() da.Request {
	return da.Request{Contract: da.Contract, Action: SaveMailDraft, Agent: "agent", Reason: "asked",
		Target: da.Target{Message: "edit-1", Recipients: []string{"A@example.invalid"}}, Content: da.Content{Text: "new text", Subject: "New"}, ExpectedVersion: oldVersion}
}

func TestDraftUpdate(t *testing.T) {
	ctx := context.Background()
	srv, opts := draftServer(t)
	got, err := Send(ctx, opts, updateRequest())
	if err != nil || got.Outcome != da.Sent || got.Effect != da.OnlyYou || !slices.Equal(srv.created, []string{`update|edit-1|W/"ck-d"|new text|New|a@example.invalid`}) {
		t.Fatalf("%+v %v %v", got, err, srv.created)
	}
	target, err := Target(ctx, opts, TargetQuery{Draft: "edit-1"})
	if err != nil || !target.Known || target.Version == oldVersion || target.Draft != "edit-1" {
		t.Fatalf("%+v %v", target, err)
	}
	for _, tc := range []struct {
		name    string
		answer  workiq.WriteAnswer
		read    *reply
		outcome da.Outcome
	}{
		{"stale", workiq.WriteAnswer{Kind: workiq.WriteRejected, Status: 412, Code: "ErrorIrresolvableConflict"}, nil, da.Failed},
		{"lost", workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_timeout"}, nil, da.Unknown},
		{"not as written", workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 200}, &reply{data: `{"isDraft":true,"subject":"Old","body":{"content":"new text"}}`}, da.Unknown},
		{"recipients kept a cc", workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 200},
			&reply{data: `{"isDraft":true,"subject":"New","toRecipients":[{"emailAddress":{"address":"a@example.invalid"}}],"ccRecipients":[{"emailAddress":{"address":"b@example.invalid"}}],"body":{"content":"new text"}}`}, da.Unknown},
		{"read fails", workiq.WriteAnswer{Kind: workiq.WriteCreated, Status: 200}, &reply{status: 503, err: errs.Usage("x")}, da.Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, opts := draftServer(t)
			srv.actAnswer = tc.answer
			if tc.read != nil {
				read := *tc.read
				srv.onAct = func() { srv.pages[editDraftRead] = read }
			}
			if got, err := Send(ctx, opts, updateRequest()); err != nil || got.Outcome != tc.outcome {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	srv, opts = draftServer(t)
	textOnly := updateRequest()
	textOnly.Target.Recipients, textOnly.Content.Subject = nil, ""
	srv.onAct = func() {
		srv.pages[editDraftRead] = reply{data: `{"isDraft":true,"subject":"Anything","ccRecipients":[{"emailAddress":{"address":"b@example.invalid"}}],"body":{"content":"new text"}}`}
	}
	if got, err := Send(ctx, opts, textOnly); err != nil || got.Outcome != da.Sent || srv.created[0] != `update|edit-1|W/"ck-d"|new text||` {
		t.Fatalf("an update of the text alone: %+v %v %v", got, err, srv.created)
	}
}

func TestDraftTargets(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		draft string
		page  *reply
		code  da.Code
	}{
		{"bad id", "edit/1", nil, da.CodeUnknownTarget},
		{"gone", "edit-1", &reply{status: 404, err: errs.Usage("x")}, da.CodeUnknownTarget},
		{"unreadable", "edit-1", &reply{data: `[]`}, da.CodeUnknownTarget},
		{"received mail", "edit-1", &reply{data: strings.Replace(oldDraft, `"isDraft":true`, `"isDraft":false`, 1)}, da.CodeTargetMismatch},
		{"stale", "edit-1", &reply{data: strings.Replace(oldDraft, `"Old"`, `"Edited"`, 1)}, da.CodeStaleVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, opts := draftServer(t)
			if tc.page != nil {
				srv.pages[editDraft] = *tc.page
			}
			req := updateRequest()
			req.Target.Message = tc.draft
			if got, err := Send(ctx, opts, req); err != nil || got.Code != tc.code || len(srv.created) != 0 {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	srv, opts := draftServer(t)
	srv.pages[editDraft] = reply{status: 503, err: errs.Usage("busy")}
	if _, err := Send(ctx, opts, updateRequest()); err == nil {
		t.Fatal("updated without reading the draft")
	}
	if _, err := Target(ctx, opts, TargetQuery{Draft: "edit-1"}); err == nil {
		t.Fatal("target read without the server")
	}
	// A draft a send recorded and has not failed belongs to that send.
	srv, opts = draftServer(t)
	srv.sendAnswer = workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_session_lost"}
	srv.replyAnswer.ServerID = "edit-1"
	srv.pages["/me/messages/edit-1?$select=id,isDraft,subject,toRecipients,ccRecipients,bccRecipients,internetMessageId,body"] = reply{data: goodDraft}
	if got, _ := Send(ctx, opts, replyRequest()); got.Outcome != da.Unknown {
		t.Fatal(got)
	}
	for _, action := range []string{SaveMailDraft, DiscardMailDraft} {
		req := updateRequest()
		req.Action = action
		if action == DiscardMailDraft {
			req.Target.Recipients, req.Content = nil, da.Content{}
		}
		if got, err := Send(ctx, opts, req); err != nil || got.Code != da.CodeTargetMismatch {
			t.Fatalf("%s changed a send's draft: %+v %v", action, got, err)
		}
	}
	if got, err := Target(ctx, opts, TargetQuery{Draft: "edit-1"}); err != nil || got.Known {
		t.Fatalf("%+v %v", got, err)
	}
	readArchive = func(context.Context, string, func(*store.Store) error) error { return errors.New("broken") }
	if _, err := readDraft(ctx, opts, fake{srv}, "edit-1"); err == nil {
		t.Fatal("read a draft without the log")
	}
}

func discardRequest() da.Request {
	return da.Request{Contract: da.Contract, Action: DiscardMailDraft, Agent: "agent", Reason: "asked", Target: da.Target{Message: "edit-1"}, ExpectedVersion: oldVersion}
}

func TestDraftDiscard(t *testing.T) {
	ctx := context.Background()
	gone := reply{status: 404, err: errs.Usage("gone")}
	for _, tc := range []struct {
		name    string
		answer  workiq.WriteAnswer
		after   *reply // the read by id after the delete
		now     *reply // the draft-state read
		outcome da.Outcome
		deleted bool
	}{
		{"deleted", workiq.WriteAnswer{Kind: workiq.WriteAccepted, Status: 204}, &gone, nil, da.Sent, true},
		{"still there after 204", workiq.WriteAnswer{Kind: workiq.WriteAccepted, Status: 204}, &reply{data: `{"id":"edit-1"}`}, nil, da.Unknown, true},
		{"refused, still a draft", workiq.WriteAnswer{Kind: workiq.WriteRejected, Status: 403, Code: "Forbidden"}, nil, nil, da.Failed, true},
		{"refused, draft gone", workiq.WriteAnswer{Kind: workiq.WriteRejected, Status: 404, Code: "ErrorItemNotFound"}, nil, &gone, da.Unknown, true},
		{"lost", workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_timeout"}, nil, nil, da.Unknown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, opts := draftServer(t)
			srv.actAnswer = tc.answer
			if tc.after != nil {
				srv.pages[editDraftGone] = *tc.after
			}
			if tc.now != nil {
				srv.pages[editDraftNow] = *tc.now
			}
			got, err := Send(ctx, opts, discardRequest())
			if err != nil || got.Outcome != tc.outcome || slices.Contains(srv.created, `delete|edit-1|W/"ck-d"`) != tc.deleted {
				t.Fatalf("%+v %v %v", got, err, srv.created)
			}
		})
	}
	// The draft is read again just before the delete; a change since the decision stops it.
	srv, opts := draftServer(t)
	reads := 0
	srv.onFetch = func(path string) {
		if path == editDraft {
			if reads++; reads == 2 {
				srv.pages[editDraft] = reply{data: strings.Replace(oldDraft, `"Old"`, `"Edited"`, 1)}
			}
		}
	}
	if got, err := Send(ctx, opts, discardRequest()); err != nil || got.Outcome != da.Failed || len(srv.created) != 0 || !strings.Contains(got.Notice, "nothing was discarded") {
		t.Fatalf("%+v %v %v", got, err, srv.created)
	}
}

func reactRequest(action string) da.Request {
	return da.Request{Contract: da.Contract, Action: action, Agent: "agent", Reason: "asked", Identity: selfAddr,
		Target: da.Target{Conversation: "48:notes", Message: "1700", Recipients: []string{selfAddr}}, Content: da.Content{Reaction: "👍"}, ExpectedVersion: "2026-10-31T11:00:00Z"}
}

func reactionServer(t *testing.T, mine string) (*server, Options) {
	t.Helper()
	srv, opts := newServer(t, true, true)
	turnOn(t, opts)
	reactions := `[]`
	if mine != "" {
		reactions = `[{"reactionType":"` + mine + `","user":{"user":{"id":"u"}}},{"reactionType":"😀","user":{"user":{"id":"other"}}}]`
	}
	srv.pages[noteMessage] = reply{data: `{"id":"1700","chatId":"48:notes","createdDateTime":"2026-10-31T11:00:00Z","reactions":` + reactions + `}`}
	srv.actAnswer = workiq.WriteAnswer{Kind: workiq.WriteAccepted, Status: 204}
	oldAttempts, oldPause := refindAttempts, refindPause
	refindAttempts, refindPause = 2, 0
	t.Cleanup(func() { refindAttempts, refindPause = oldAttempts, oldPause })
	return srv, opts
}

func TestReactions(t *testing.T) {
	ctx := context.Background()
	srv, opts := reactionServer(t, "")
	srv.onAct = func() {
		srv.pages[noteMessage] = reply{data: `{"id":"1700","chatId":"48:notes","createdDateTime":"2026-10-31T11:00:00Z","reactions":[{"reactionType":"👍️","user":{"user":{"id":"u"}}}]}`}
	}
	got, err := Send(ctx, opts, reactRequest(AddReaction))
	if err != nil || got.Outcome != da.Sent || got.Verification != "found" || !slices.Equal(srv.created, []string{"react|48:notes|1700|👍"}) {
		t.Fatalf("%+v %v %v", got, err, srv.created)
	}
	if row := logged(t, opts)[0]; row.Reaction != "👍" || row.Text != "" {
		t.Fatalf("%+v", row)
	}
	target, err := Target(ctx, opts, TargetQuery{Chat: "48:notes", ChatMessage: "1700"})
	if err != nil || !target.Known || target.Version != "2026-10-31T11:00:00Z" || target.WatermarkAt != "" || !slices.Equal(target.Recipients, []string{selfAddr}) {
		t.Fatalf("%+v %v", target, err)
	}
	srv.onAct = func() {
		srv.pages[noteMessage] = reply{data: `{"id":"1700","chatId":"48:notes","createdDateTime":"2026-10-31T11:00:00Z","reactions":[]}`}
	}
	if got, err := Send(ctx, opts, reactRequest(RemoveReaction)); err != nil || got.Outcome != da.Sent || srv.created[1] != "unreact|48:notes|1700|👍" {
		t.Fatalf("%+v %v %v", got, err, srv.created)
	}
	srv, opts = reactionServer(t, "👍")
	if got, err := Send(ctx, opts, reactRequest(AddReaction)); err != nil || got.Outcome != da.Sent || len(srv.created) != 0 || !strings.Contains(got.Notice, "already") {
		t.Fatalf("%+v %v", got, err)
	}
	for _, tc := range []struct {
		name    string
		answer  workiq.WriteAnswer
		outcome da.Outcome
	}{
		{"refused", workiq.WriteAnswer{Kind: workiq.WriteRejected, Status: 400, Code: "BadRequest"}, da.Failed},
		{"accepted but not seen", workiq.WriteAnswer{Kind: workiq.WriteAccepted, Status: 204}, da.Unknown},
		{"lost", workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_timeout"}, da.Unknown},
	} {
		srv, opts := reactionServer(t, "")
		srv.actAnswer = tc.answer
		if got, err := Send(ctx, opts, reactRequest(AddReaction)); err != nil || got.Outcome != tc.outcome || len(srv.created) != 1 {
			t.Fatalf("%s: %+v %v", tc.name, got, err)
		}
	}
	srv, opts = reactionServer(t, "")
	srv.actAnswer = workiq.WriteAnswer{Kind: workiq.WriteRejected, Status: 429, Code: "TooManyRequests"}
	srv.onAct = func() {
		srv.pages[noteMessage] = reply{data: `{"id":"1700","chatId":"48:notes","createdDateTime":"2026-10-31T11:00:00Z","reactions":[{"reactionType":"👍","user":{"user":{"id":"u"}}}]}`}
	}
	if got, err := Send(ctx, opts, reactRequest(AddReaction)); err != nil || got.Outcome != da.Sent {
		t.Fatalf("a refusal after the reaction landed: %+v %v", got, err)
	}
}

func TestReactionTargetsAndInput(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		message string
		page    *reply
		code    da.Code
	}{
		{"bad id", "17a", nil, da.CodeUnknownTarget},
		{"gone", "1700", &reply{status: 404, err: errs.Usage("x")}, da.CodeUnknownTarget},
		{"unreadable", "1700", &reply{data: `[]`}, da.CodeUnknownTarget},
		{"other chat", "1700", &reply{data: `{"id":"1700","chatId":"19:x@thread.v2","createdDateTime":"2026-10-31T11:00:00Z"}`}, da.CodeTargetMismatch},
		{"edited since", "1700", &reply{data: `{"id":"1700","createdDateTime":"2026-10-31T11:00:00Z","lastEditedDateTime":"2026-10-31T12:00:00Z"}`}, da.CodeStaleVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, opts := reactionServer(t, "")
			if tc.page != nil {
				srv.pages[noteMessage] = *tc.page
			}
			req := reactRequest(AddReaction)
			req.Target.Message = tc.message
			if got, err := Send(ctx, opts, req); err != nil || got.Code != tc.code || len(srv.created) != 0 {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	srv, opts := reactionServer(t, "")
	srv.pages[noteMessage] = reply{status: 503, err: errs.Usage("busy")}
	if _, err := Send(ctx, opts, reactRequest(AddReaction)); err == nil {
		t.Fatal("reacted without reading the message")
	}
	if _, err := Target(ctx, opts, TargetQuery{Chat: "48:notes", ChatMessage: "1700"}); err == nil {
		t.Fatal("target read without the server")
	}
	for _, reaction := range []string{"like", "👍 ", "a👍", strings.Repeat("👍", maxReaction+1), "👍\u202e", "\xff"} {
		req := reactRequest(AddReaction)
		req.Content.Reaction = reaction
		if reaction == "👍 " {
			continue // surrounding white space is trimmed, as everywhere in the contract
		}
		if got, err := Send(ctx, opts, req); err != nil || got.Field != "content.reaction" {
			t.Fatalf("%q: %+v %v", reaction, got, err)
		}
	}
	if !validReaction("❤️") || !validReaction("👩‍💻") {
		t.Fatal("refused an emoji")
	}
}

func TestVerifyOtherActions(t *testing.T) {
	ctx := context.Background()
	// An unknown update becomes sent once the draft reads as written.
	srv, opts := draftServer(t)
	srv.actAnswer = workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_timeout"}
	srv.onAct = func() { srv.pages[editDraftRead] = reply{status: 503, err: errs.Usage("x")} }
	if got, _ := Send(ctx, opts, updateRequest()); got.Outcome != da.Unknown {
		t.Fatal(got)
	}
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Unknown {
		t.Fatal(got, err)
	}
	srv.pages[editDraftRead] = reply{data: `{"isDraft":true,"subject":"New","toRecipients":[{"emailAddress":{"address":"a@example.invalid"}}],"body":{"content":"new text"}}`}
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Sent {
		t.Fatal(got, err)
	}
	// An unknown discard: gone is done; still a draft ten minutes on is not done.
	srv, opts = draftServer(t)
	srv.actAnswer = workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_timeout"}
	if got, _ := Send(ctx, opts, discardRequest()); got.Outcome != da.Unknown {
		t.Fatal(got)
	}
	srv.pages[editDraftGone] = reply{data: `{"id":"edit-1"}`}
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Unknown {
		t.Fatal(got, err)
	}
	now = later(11 * time.Minute)
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Failed || got.Verification != "not_sent_proven" {
		t.Fatal(got, err)
	}
	now = time.Now
	srv, opts = draftServer(t)
	srv.actAnswer = workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_timeout"}
	if got, _ := Send(ctx, opts, discardRequest()); got.Outcome != da.Unknown {
		t.Fatal(got)
	}
	srv.pages[editDraftGone] = reply{status: 404, err: errs.Usage("gone")}
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Sent {
		t.Fatal(got, err)
	}
	// An unknown reaction becomes sent once the message shows it.
	srv, opts = reactionServer(t, "")
	srv.actAnswer = workiq.WriteAnswer{Kind: workiq.WriteUnknown, Code: "workiq_timeout"}
	if got, _ := Send(ctx, opts, reactRequest(AddReaction)); got.Outcome != da.Unknown {
		t.Fatal(got)
	}
	srv.pages[noteMessage] = reply{data: `{"id":"1700","chatId":"48:notes","createdDateTime":"2026-10-31T11:00:00Z","reactions":[{"reactionType":"👍","user":{"user":{"id":"u"}}}]}`}
	if got, err := Verify(ctx, opts, 1); err != nil || got.Outcome != da.Sent {
		t.Fatal(got, err)
	}
}

var oldVersion = draftDigest("Old", "text", "old", nil, nil, nil)
