package directedwrites

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	da "github.com/ourostack/m365crawl/internal/directedactions"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

const (
	DiscardMailDraft = "discard_mail_draft"
	AddReaction      = "add_reaction"
	RemoveReaction   = "remove_reaction"
	maxReaction      = 16
)

func isReaction(action string) bool { return action == AddReaction || action == RemoveReaction }

// validReaction says reaction is an emoji Teams takes: no ASCII (named reactions such as "like" are
// refused by Graph) and none of the forbidden characters.
func validReaction(reaction string) bool {
	if reaction == "" || !utf8.ValidString(reaction) || utf8.RuneCountInString(reaction) > maxReaction {
		return false
	}
	return !strings.ContainsFunc(reaction, func(r rune) bool { return r < 0x80 || forbidden(r, true) })
}

// sameReaction compares reactions without the emoji presentation selector, which Graph may drop.
func sameReaction(a, b string) bool {
	return strings.ReplaceAll(a, "\ufe0f", "") == strings.ReplaceAll(b, "\ufe0f", "")
}

// retry reads until check holds, waiting a little between reads, as a change may not show at once.
func retry(check func() bool) bool {
	for attempt := 0; attempt < refindAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(refindPause)
		}
		if check() {
			return true
		}
	}
	return false
}

// draftTarget is what an update or a discard knows about the draft it names.
type draftTarget struct {
	known         bool
	kind          da.Kind
	version, etag string
}

// readDraft reads a draft of the person's mailbox by its Graph id. A mail that is not a draft, or a
// draft a directed send recorded and has not failed, is not a draft another write may change.
func readDraft(ctx context.Context, opts Options, s session, id string) (draftTarget, error) {
	if !workiq.ValidMessageID(id) {
		return draftTarget{}, nil
	}
	page, err := s.Fetch(ctx, "/me/messages/"+id+"?$select=id,isDraft,subject,toRecipients,ccRecipients,bccRecipients,body")
	if gone(page, err) {
		return draftTarget{}, nil
	}
	if err != nil {
		return draftTarget{}, err
	}
	var m struct {
		IsDraft                                   bool
		Subject                                   string
		ETag                                      string `json:"@odata.etag"`
		Body                                      struct{ ContentType, Content string }
		ToRecipients, CcRecipients, BccRecipients []address
	}
	if json.Unmarshal(page.Data, &m) != nil {
		return draftTarget{}, nil
	}

	held := false
	if err := readArchive(ctx, opts.DBPath, func(st *store.Store) (err error) {
		held, err = st.DirectedDraftHeld(ctx, id)
		return err
	}); err != nil {
		return draftTarget{}, err
	}
	t := draftTarget{known: true, kind: da.KindMailDraft, version: draftDigest(m.Subject, m.Body.ContentType, m.Body.Content,
		addresses(m.ToRecipients), addresses(m.CcRecipients), addresses(m.BccRecipients)), etag: m.ETag}
	if !m.IsDraft || held {
		t.kind = da.KindMailMessage
	}
	return t, nil
}

// draftDigest is a draft's version. Exchange bumps a draft's changeKey on its own for a while after it
// is saved (seen live), so the version is a digest of what the draft says, which only an edit changes.
func draftDigest(subject, contentType, content string, to, cc, bcc []string) string {
	digest := sha256.New()
	for _, part := range [][]string{{subject, contentType, content}, to, cc, bcc} {
		for _, v := range part {
			digest.Write([]byte(v))
			digest.Write([]byte{0})
		}
		digest.Write([]byte{1})
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))[:32]
}

// bodyIs says a whole body is text. An update replaces the body, so unlike a reply's check, a body
// that only starts with the text does not prove the update applied.
func bodyIs(contentType, content, text string) bool {
	if strings.EqualFold(contentType, "html") {
		content = outlookmail.HTMLText([]byte(content))
	}
	return collapse(content) == collapse(workiq.ScrubText(text))
}

// draftUpdated says the draft now holds what the update wrote.
func draftUpdated(ctx context.Context, s session, row store.DirectedWrite, p person) bool {
	page, err := s.Fetch(ctx, "/me/messages/"+row.Message+"?$select=id,isDraft,subject,toRecipients,ccRecipients,bccRecipients,body")
	var m struct {
		IsDraft                                   bool
		Subject                                   string
		Body                                      struct{ ContentType, Content string }
		ToRecipients, CcRecipients, BccRecipients []address
	}
	if err != nil || json.Unmarshal(page.Data, &m) != nil || !m.IsDraft || !bodyIs(m.Body.ContentType, m.Body.Content, row.Text) {
		return false
	}
	if row.Subject != "" && !sameText(row.Subject, m.Subject) {
		return false
	}
	return len(row.Recipients) == 0 || sameSet(p.own(addresses(m.ToRecipients)), row.Recipients) && len(m.CcRecipients) == 0 && len(m.BccRecipients) == 0
}

// draftGone says a read of the draft by id answered that it is not there.
func draftGone(ctx context.Context, s session, id string) bool {
	page, err := s.Fetch(ctx, "/me/messages/"+id+"?$select=id")
	return err != nil && page.StatusCode == 404
}

// chatMessage is what a reaction knows about the message it names.
type chatMessage struct {
	known         bool
	chat, version string
	mine          []string // the person's reactions on it
}

func (m chatMessage) has(reaction string) bool {
	return slices.ContainsFunc(m.mine, func(r string) bool { return sameReaction(r, reaction) })
}

// readChatMessage reads one message of a chat. Its version is when its content last changed (its
// last edit, else its creation), so reactions, the agent's own included, do not make it stale.
func readChatMessage(ctx context.Context, s session, chat, id string, p person) (chatMessage, error) {
	if !workiq.ValidChatID(chat) || !workiq.ValidChatMessageID(id) {
		return chatMessage{}, nil
	}
	page, err := s.Fetch(ctx, "/chats/"+chat+"/messages/"+id)
	if gone(page, err) {
		return chatMessage{}, nil
	}
	if err != nil {
		return chatMessage{}, err
	}
	var m struct {
		ChatID, CreatedDateTime, LastEditedDateTime string
		Reactions                                   []struct {
			ReactionType string
			User         struct{ User struct{ ID string } }
		}
	}
	if json.Unmarshal(page.Data, &m) != nil {
		return chatMessage{}, nil
	}
	c := chatMessage{known: true, chat: m.ChatID, version: m.LastEditedDateTime}
	if c.chat == "" {
		c.chat = chat
	}
	if c.version == "" {
		c.version = m.CreatedDateTime
	}
	for _, r := range m.Reactions {
		if r.User.User.ID == p.user.ID {
			c.mine = append(c.mine, r.ReactionType)
		}
	}
	return c, nil
}

// inState says the person's reaction is on the message (add) or not (remove).
func inState(ctx context.Context, s session, row store.DirectedWrite, p person) bool {
	m, err := readChatMessage(ctx, s, row.Conversation, row.Message, p)
	return err == nil && m.known && m.has(row.Reaction) == (row.Action == AddReaction)
}

func finisher(ctx context.Context, opts Options, row store.DirectedWrite) func(store.DirectedWriteChange, string) Result {
	return func(change store.DirectedWriteChange, notice string) Result {
		r := save(ctx, opts, row, "in_flight", change)
		r.Decision = da.Accept
		if r.Notice == "" {
			r.Notice = notice
		}
		return r
	}
}

func answered(answer workiq.WriteAnswer) store.DirectedWriteChange {
	return store.DirectedWriteChange{Outcome: string(da.Unknown), Phase: "answered", ServerStatus: answer.Status, ErrorCode: answer.Code, Cause: "server_" + string(answer.Kind)}
}

func observed(change store.DirectedWriteChange, cause string) store.DirectedWriteChange {
	change.Outcome, change.Phase, change.Verification, change.Cause = string(da.Sent), "verified", "found", cause
	return change
}

// updateDraft writes the request into the draft, only while it is the version the decision read.
func updateDraft(ctx context.Context, opts Options, s session, row store.DirectedWrite, t draftTarget, p person) Result {
	finish := finisher(ctx, opts, row)
	answer := s.UpdateDraft(ctx, row.Message, t.etag, workiq.DraftChange{Text: row.Text, Subject: row.Subject, SetSubject: row.Subject != "",
		To: row.Recipients, SetTo: len(row.Recipients) > 0})
	change := answered(answer)
	switch answer.Kind {
	case workiq.WriteRejected:
		// An update applies whole or not at all, so a refused one (a stale version included) did nothing.
		change.Outcome = string(da.Failed)
	case workiq.WriteCreated, workiq.WriteAccepted:
		if retry(func() bool { return draftUpdated(ctx, s, row, p) }) {
			change = observed(change, "verified_by_read")
		}
	}
	return finish(change, "")
}

// discardDraft deletes the draft after reading it again: WorkIQ's delete does not honor If-Match.
func discardDraft(ctx context.Context, opts Options, s session, row store.DirectedWrite, t draftTarget) Result {
	finish := finisher(ctx, opts, row)
	current, err := readDraft(ctx, opts, s, row.Message)
	if err != nil || current.kind != da.KindMailDraft || current.version != t.version {
		return finish(store.DirectedWriteChange{Outcome: string(da.Failed), Phase: "answered", ErrorCode: "draft_changed", Cause: "not_discarded"},
			"nothing was discarded: the draft changed or could not be read again")
	}
	answer := s.DeleteDraft(ctx, row.Message, current.etag)
	change := answered(answer)
	switch answer.Kind {
	case workiq.WriteAccepted, workiq.WriteCreated:
		if retry(func() bool { return draftGone(ctx, s, row.Message) }) {
			change = observed(change, "gone_by_read")
		}
	case workiq.WriteRejected:
		if stillDraft(ctx, s, row.Message) {
			change.Outcome = string(da.Failed)
		}
	}
	return finish(change, "")
}

// react sets or removes the person's reaction, then reads the message to see it.
func react(ctx context.Context, opts Options, s session, row store.DirectedWrite, m chatMessage, p person) Result {
	finish := finisher(ctx, opts, row)
	if m.has(row.Reaction) == (row.Action == AddReaction) {
		return finish(observed(store.DirectedWriteChange{}, "already_in_state"), "the reaction was already as asked; nothing was changed")
	}
	answer := s.SetReaction(ctx, row.Conversation, row.Message, row.Reaction, row.Action == RemoveReaction)
	change := answered(answer)
	attempts := func() bool { return inState(ctx, s, row, p) }
	if answer.Kind == workiq.WriteRejected {
		if attempts() {
			return finish(observed(change, "verified_by_read"), "")
		}
		change.Outcome = string(da.Failed)
		return finish(change, "")
	}
	if retry(attempts) {
		change = observed(change, "verified_by_read")
	}
	return finish(change, "")
}

// verifyOther settles an unknown draft update, discard or reaction by reading its target.
func verifyOther(ctx context.Context, opts Options, s session, w store.DirectedWrite, p person) Result {
	var change store.DirectedWriteChange
	switch {
	case w.Action == SaveMailDraft && draftUpdated(ctx, s, w, p), isReaction(w.Action) && inState(ctx, s, w, p):
		change = observed(change, "verified_by_read")
	case w.Action == DiscardMailDraft && draftGone(ctx, s, w.Message):
		change = observed(change, "gone_by_read")
	case w.Action == DiscardMailDraft && now().Sub(w.CreatedAt) >= notSentAfter && stillDraft(ctx, s, w.Message):
		change = store.DirectedWriteChange{Outcome: string(da.Failed), Phase: "verified", Verification: "not_sent_proven", Cause: "draft_still_there"}
	default:
		return ofWrite(w)
	}
	change.ErrorCode = w.ErrorCode
	return save(ctx, opts, w, w.Phase, change)
}
