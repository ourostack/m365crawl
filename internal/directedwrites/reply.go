package directedwrites

import (
	"context"
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	da "github.com/ourostack/m365crawl/internal/directedactions"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

const (
	ReplyMail    = "reply_mail"
	ReplyAllMail = "reply_all_mail"
	SendMail     = "send_mail"
)

// throughDraft says an action sends mail by making a draft, checking it and sending it.
func throughDraft(action string) bool {
	return action == ReplyMail || action == ReplyAllMail || action == SendMail
}

var (
	internetMessageID = regexp.MustCompile(`^<[!-;=?-~]{1,994}@[!-;=?-~]{1,994}>$`)
	horizontalRule    = regexp.MustCompile(`(?i)<hr[\s/>]`)
	// refindAttempts and refindPause bound waiting for a sent reply to reach Sent Items.
	refindAttempts = 6
	refindPause    = 2 * time.Second
)

// mail is what a reply knows about the mail it answers.
type mail struct {
	known      bool
	kind       da.Kind
	id         string // the Graph id of the copy the reply is made from
	recipients []string
	version    string
}

type address struct{ EmailAddress struct{ Address string } }

type mailCopy struct {
	ID, ParentFolderID, ChangeKey, Subject string
	IsDraft                                bool
	From                                   *address
	ReplyTo, ToRecipients, CcRecipients    []address
}

// byInternetMessageID lists the mail with one Internet Message-ID, in every folder or in one.
func byInternetMessageID(folder, id, fields string) string {
	base := "/me/messages"
	if folder != "" {
		base = "/me/mailFolders/" + folder + "/messages"
	}
	filter := "internetMessageId eq '" + strings.ReplaceAll(id, "'", "''") + "'"
	return base + "?$filter=" + strings.ReplaceAll(url.QueryEscape(filter), "+", "%20") + "&$select=" + fields
}

func (p person) own(addresses []string) []string {
	out := make([]string, len(addresses))
	for i, a := range addresses {
		out[i] = strings.ToLower(strings.TrimSpace(a))
		if p.alias(a) {
			out[i] = p.canonical
		}
	}
	return out
}

func addresses(list []address) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.EmailAddress.Address)
	}
	return out
}

// readMail finds the received copy of the mail with Internet Message-ID id. Its audience is who a
// reply reaches: its replyTo addresses, else its sender, and for a reply-all (all) also its To and Cc
// recipients other than the person. A mail found only in Sent Items, copies that disagree, or a
// sender without an address are not a known target.
func readMail(ctx context.Context, s session, id string, p person, all bool) (mail, error) {
	if !internetMessageID.MatchString(id) {
		return mail{}, nil
	}
	page, err := s.Fetch(ctx, byInternetMessageID("", id, "id,parentFolderId,changeKey,isDraft,from,replyTo,toRecipients,ccRecipients,subject"))
	if gone(page, err) {
		return mail{}, nil
	}
	if err != nil {
		return mail{}, err
	}
	var list struct{ Value []mailCopy }
	if json.Unmarshal(page.Data, &list) != nil || page.NextCursor != "" {
		return mail{}, nil
	}
	folder, err := s.Fetch(ctx, "/me/mailFolders/sentitems?$select=id")
	if err != nil {
		return mail{}, err
	}
	var sent struct{ ID string }
	if json.Unmarshal(folder.Data, &sent) != nil || sent.ID == "" {
		return mail{}, nil // without the Sent Items id a sent copy cannot be told from a received one
	}
	var received []mailCopy
	drafts := 0
	for _, c := range list.Value {
		switch {
		case c.IsDraft:
			drafts++
		case c.ParentFolderID != sent.ID:
			received = append(received, c)
		}
	}
	if len(received) == 0 {
		if drafts > 0 && drafts == len(list.Value) {
			return mail{known: true, kind: da.KindMailDraft}, nil
		}
		return mail{}, nil
	}
	slices.SortFunc(received, func(a, b mailCopy) int { return strings.Compare(a.ID, b.ID) })
	audience := func(c mailCopy) []string {
		var base []string
		switch {
		case len(c.ReplyTo) > 0:
			base = p.own(addresses(c.ReplyTo))
		case c.From != nil:
			base = p.own([]string{c.From.EmailAddress.Address})
		default:
			return nil
		}
		if all {
			for _, a := range append(addresses(c.ToRecipients), addresses(c.CcRecipients)...) {
				if !p.alias(a) {
					base = append(base, strings.ToLower(strings.TrimSpace(a)))
				}
			}
			slices.Sort(base)
			base = slices.Compact(base)
		}
		return base
	}
	chosen := received[0]
	recipients := audience(chosen)
	for _, c := range received {
		if c.Subject != chosen.Subject || !slices.Equal(audience(c), recipients) {
			return mail{}, nil
		}
	}
	if len(recipients) == 0 || slices.Contains(recipients, "") || !workiq.ValidMessageID(chosen.ID) {
		return mail{}, nil
	}
	return mail{known: true, kind: da.KindMailMessage, id: chosen.ID, recipients: recipients, version: chosen.ChangeKey}, nil
}

// replyHTML is the reply's text as the HTML comment createReply takes.
func replyHTML(text string) string {
	return strings.ReplaceAll(html.EscapeString(text), "\n", "<br>")
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// draftMatches reads the draft back and says whether it is still a draft, addressed to exactly the
// decided recipients (no Bcc, and Cc only for a reply-all), with the request's subject for new mail
// and its text above any quoted mail.
func draftMatches(ctx context.Context, s session, draft string, row store.DirectedWrite, p person) bool {
	page, err := s.Fetch(ctx, "/me/messages/"+draft+"?$select=id,isDraft,subject,toRecipients,ccRecipients,bccRecipients,internetMessageId,body")
	if err != nil {
		return false
	}
	var m struct {
		IsDraft                                   bool
		InternetMessageID, Subject                string
		Body                                      struct{ ContentType, Content string }
		ToRecipients, CcRecipients, BccRecipients []address
	}
	if json.Unmarshal(page.Data, &m) != nil {
		return false
	}
	all := append(addresses(m.ToRecipients), addresses(m.CcRecipients)...)
	if !m.IsDraft || m.InternetMessageID == "" || !sameSet(p.own(all), row.Recipients) || len(m.BccRecipients) > 0 ||
		len(m.CcRecipients) > 0 && row.Action != ReplyAllMail || row.Action == SendMail && !sameText(row.Subject, m.Subject) {
		return false
	}
	return bodyMatches(m.Body.ContentType, m.Body.Content, row.Text)
}

// bodyMatches says a mail body holds text: the text above its first rule, or, without one, at its start.
func bodyMatches(contentType, content, text string) bool {
	want := collapse(workiq.ScrubText(text))
	if !strings.EqualFold(contentType, "html") {
		return strings.HasPrefix(collapse(content), want)
	}
	if at := horizontalRule.FindStringIndex(content); at != nil {
		return collapse(outlookmail.HTMLText([]byte(content[:at[0]]))) == want
	}
	return strings.HasPrefix(collapse(outlookmail.HTMLText([]byte(content))), want)
}

// stillDraft says a read of the draft by id answered that it is still an unsent draft.
func stillDraft(ctx context.Context, s session, draft string) bool {
	page, err := s.Fetch(ctx, "/me/messages/"+draft+"?$select=id,isDraft")
	var m struct{ IsDraft bool }
	return err == nil && json.Unmarshal(page.Data, &m) == nil && m.IsDraft
}

// refind looks for the sent reply in Sent Items by its Internet Message-ID, waiting a little for it
// to arrive. same says exactly one copy is there, addressed to exactly the decided recipients.
func refind(ctx context.Context, s session, imid string, recipients []string, p person) (id string, same, found bool) {
	for attempt := 0; attempt < refindAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(refindPause)
		}
		page, err := s.Fetch(ctx, byInternetMessageID("sentitems", imid, "id,internetMessageId,toRecipients,ccRecipients,bccRecipients"))
		if err != nil {
			continue
		}
		var list struct {
			Value []struct {
				ID                                        string
				ToRecipients, CcRecipients, BccRecipients []address
			}
		}
		if json.Unmarshal(page.Data, &list) != nil || len(list.Value) == 0 {
			continue
		}
		c := list.Value[0]
		all := append(append(addresses(c.ToRecipients), addresses(c.CcRecipients)...), addresses(c.BccRecipients)...)
		return c.ID, len(list.Value) == 1 && sameSet(p.own(all), recipients), true
	}
	return "", false, false
}

// stillInFlight re-reads a write just before a server call that may act.
func stillInFlight(ctx context.Context, opts Options, id int64) (store.DirectedWrite, bool, error) {
	var current store.DirectedWrite
	err := readArchive(ctx, opts.DBPath, func(st *store.Store) (err error) {
		current, _, err = st.DirectedWriteByID(ctx, id)
		return err
	})
	return current, err == nil && current.Outcome == string(da.Unknown) && current.Phase == "in_flight", err
}

func draftOf(ctx context.Context, opts Options, id int64) (draft, imid string, ok bool, err error) {
	err = readArchive(ctx, opts.DBPath, func(st *store.Store) (err error) {
		draft, imid, ok, err = st.DirectedDraft(ctx, id)
		return err
	})
	return draft, imid, ok, err
}

// sendReply makes the draft (a reply, a reply-all or new mail), records it, checks it and only then
// sends it.
func sendReply(ctx context.Context, opts Options, s session, row store.DirectedWrite, m mail, p person) Result {
	finish := func(change store.DirectedWriteChange, notice string, tell bool) Result {
		r := save(ctx, opts, row, "in_flight", change)
		r.Decision = da.Accept
		if r.Notice == "" {
			r.Notice = notice
		}
		r.TellPerson = r.TellPerson || tell
		return r
	}
	notSent := func(code, cause, notice string, status int) Result {
		return finish(store.DirectedWriteChange{Outcome: string(da.Failed), Phase: "answered", ServerStatus: status, ErrorCode: code, Cause: cause}, notice, false)
	}
	var made workiq.WriteAnswer
	switch row.Action {
	case ReplyMail:
		made = s.CreateReply(ctx, m.id, replyHTML(row.Text))
	case ReplyAllMail:
		made = s.CreateReplyAll(ctx, m.id, replyHTML(row.Text))
	default:
		made = s.CreateMailDraft(ctx, row.Subject, row.Text, row.Recipients)
	}
	if made.Kind != workiq.WriteCreated || !made.IsDraft || made.InternetMessageID == "" || !workiq.ValidMessageID(made.ServerID) {
		notice := "nothing was sent; a draft may be in the person's Drafts"
		if made.ServerID != "" {
			notice = "nothing was sent; draft " + made.ServerID + " stays in the person's Drafts"
		}
		code := made.Code
		if made.Kind == workiq.WriteCreated {
			code = "reply_draft_unusable"
		}
		return notSent(code, "reply_draft_not_made", notice, made.Status)
	}
	draft := made.ServerID
	leftover := "nothing was sent; draft " + draft + " stays in the person's Drafts"
	var recorded bool
	err := writeArchive(ctx, opts.DBPath, func(ctx context.Context, st *store.Store) (err error) {
		recorded, err = st.RecordDirectedDraft(ctx, row.ID, draft, made.InternetMessageID)
		return err
	})
	if err != nil {
		return notSent("write_log_unreadable", "draft_not_recorded", leftover+"; it could not be recorded", 0)
	}
	if !recorded {
		current, _, err := stillInFlight(ctx, opts, row.ID)
		if err != nil {
			current = row
		}
		settled := ofWrite(current)
		settled.Decision, settled.Notice = da.Accept, "the write changed before it was sent; "+leftover
		return settled
	}
	if !draftMatches(ctx, s, draft, row, p) {
		return notSent("reply_draft_mismatch", "draft_mismatch", leftover+": it did not match the request", 0)
	}
	if current, live, err := stillInFlight(ctx, opts, row.ID); err != nil {
		return notSent("write_log_unreadable", "not_sent", leftover, 0)
	} else if !live {
		settled := ofWrite(current)
		settled.Decision, settled.Notice = da.Accept, "the write changed before it was sent; "+leftover
		return settled
	}
	sent := s.SendDraft(ctx, draft)
	change := store.DirectedWriteChange{Outcome: string(da.Unknown), Phase: "answered", ServerStatus: sent.Status, ErrorCode: sent.Code, Cause: "server_" + string(sent.Kind)}
	notice, tell := "", false
	switch sent.Kind {
	case workiq.WriteAccepted:
		if id, same, found := refind(ctx, s, made.InternetMessageID, row.Recipients, p); found {
			change.Outcome, change.Phase, change.Verification, change.ServerID, change.Cause = string(da.Sent), "verified", "found", id, "found_in_sent_items"
			if !same {
				notice, tell = "the reply in Sent Items is not addressed exactly as the request was", true
			}
		}
	case workiq.WriteRejected:
		// A refused send proves nothing on its own (a retried request finds the draft gone); only the
		// draft, still unsent, does.
		if stillDraft(ctx, s, draft) {
			change.Outcome = string(da.Failed)
			notice = leftover
		}
	}
	return finish(change, notice, tell)
}

// verifyReply settles an unknown reply: found in Sent Items is sent; ten minutes on, no recorded
// draft, or a draft still unsent after Sent Items was searched, is not sent.
func verifyReply(ctx context.Context, opts Options, s session, w store.DirectedWrite, p person) (Result, error) {
	draft, imid, recorded, err := draftOf(ctx, opts, w.ID)
	if err != nil {
		return Result{}, err
	}
	late := now().Sub(w.CreatedAt) >= notSentAfter
	var change store.DirectedWriteChange
	notice, tell := "", false
	switch {
	case !recorded && late:
		change = store.DirectedWriteChange{Outcome: string(da.Failed), Phase: "verified", Verification: "not_sent_proven", Cause: "no_draft_recorded"}
	case !recorded:
		return ofWrite(w), nil
	default:
		if id, same, found := refind(ctx, s, imid, w.Recipients, p); found {
			change = store.DirectedWriteChange{Outcome: string(da.Sent), Phase: "verified", Verification: "found", ServerID: id, Cause: "found_in_sent_items"}
			if !same {
				notice, tell = "the reply in Sent Items is not addressed exactly as the request was", true
			}
		} else if late && stillDraft(ctx, s, draft) {
			change = store.DirectedWriteChange{Outcome: string(da.Failed), Phase: "verified", Verification: "not_sent_proven", Cause: "draft_still_unsent"}
		} else {
			return ofWrite(w), nil
		}
	}
	change.ErrorCode = w.ErrorCode
	r := save(ctx, opts, w, w.Phase, change)
	if r.Notice == "" {
		r.Notice = notice
	}
	r.TellPerson = r.TellPerson || tell
	return r, nil
}
