package directedwrites

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	da "github.com/ourostack/m365crawl/internal/directedactions"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

// Write reads one write from the log; a missing id is a usage error.
func Write(ctx context.Context, opts Options, id int64) (store.DirectedWrite, error) {
	var w store.DirectedWrite
	found := false
	err := readArchive(ctx, opts.DBPath, func(s *store.Store) (err error) {
		w, found, err = s.DirectedWriteByID(ctx, id)
		return err
	})
	if err == nil && !found || errors.Is(err, store.ErrNoArchive) {
		return w, errs.Usage("there is no directed write " + strconv.FormatInt(id, 10) + "; `m365crawl writes status` lists them")
	}
	return w, err
}

// Verify re-reads the server to settle an unknown write by proof. Any other write is reported as it
// stands, without WorkIQ.
func Verify(ctx context.Context, opts Options, id int64) (Result, error) {
	w, err := Write(ctx, opts, id)
	if err != nil {
		return Result{}, err
	}
	if w.Outcome != string(da.Unknown) || w.Action == SaveMailDraft && w.ServerID == "" {
		return ofWrite(w), nil
	}
	if err := workIQEnabled(ctx, opts); err != nil {
		return Result{}, err
	}
	s, user, err := openAs(ctx, opts, w.AccountPin)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = s.Close() }()
	if user.ID != w.UserID {
		return Result{}, identityChanged()
	}
	if w.Action == ReplyMail {
		return verifyReply(ctx, opts, s, w, person{user: user, canonical: w.Identity, pin: w.AccountPin})
	}
	var change store.DirectedWriteChange
	switch {
	case w.ServerID != "":
		found, err := verifyByID(ctx, s, w, user.ID)
		if err != nil || !found {
			return ofWrite(w), nil
		}
		change = store.DirectedWriteChange{Outcome: string(da.Sent), Phase: "verified", Verification: "found", Cause: "verified_by_read"}
	default:
		rows, err := earlierWrites(ctx, opts)
		if err != nil {
			return Result{}, err
		}
		claimed := map[string]bool{}
		for _, other := range rows {
			if other.ID != w.ID && other.Action == w.Action && other.Conversation == w.Conversation && other.ServerID != "" {
				claimed[other.ServerID] = true
			}
		}
		found, proof, err := search(ctx, s, w, user.ID, claimed)
		switch {
		case err != nil:
			return ofWrite(w), nil
		case found != "":
			change = store.DirectedWriteChange{Outcome: string(da.Sent), Phase: "verified", Verification: "found", ServerID: found, Cause: "found_after_watermark"}
		case proof && now().Sub(w.CreatedAt) >= notSentAfter:
			change = store.DirectedWriteChange{Outcome: string(da.Failed), Phase: "verified", Verification: "not_sent_proven", Cause: "nothing_after_watermark"}
		default:
			return ofWrite(w), nil
		}
	}
	change.ErrorCode = w.ErrorCode
	return save(ctx, opts, w, w.Phase, change), nil
}

// search lists the chat's messages newer than the write's watermark. found is the one matching
// message from the person that no other write holds; proof says the listing reached the watermark
// and holds no newer message from the person at all, deleted or not.
func search(ctx context.Context, s session, w store.DirectedWrite, userID string, claimed map[string]bool) (found string, proof bool, err error) {
	var mark time.Time
	if w.WatermarkAt != "" {
		if mark, err = time.Parse(time.RFC3339Nano, w.WatermarkAt); err != nil {
			return "", false, err
		}
	}
	path, reached, fromPerson := "/chats/"+w.Conversation+"/messages?$top=50&$orderby=createdDateTime%20desc", false, 0
	var matches []string
	for page := 0; page < maxPages && !reached; page++ {
		result, err := s.Fetch(ctx, path)
		if err != nil {
			return "", false, err
		}
		var list struct{ Value []json.RawMessage }
		if err := json.Unmarshal(result.Data, &list); err != nil {
			return "", false, err
		}
		for _, raw := range list.Value {
			m, err := workiq.DecodeWrittenMessage(ctx, raw)
			if err != nil {
				return "", false, err
			}
			newer := m.Created.IsZero() || m.Created.After(mark) || m.Created.Equal(mark) && m.ID != w.WatermarkID
			if !newer {
				reached = true
				break
			}
			if m.FromUserID != userID {
				continue
			}
			fromPerson++
			if !m.Deleted && !claimed[m.ID] && sameText(w.Text, m.Text) {
				matches = append(matches, m.ID)
			}
		}
		if result.NextCursor == "" {
			reached = true
		}
		path = result.NextCursor
	}
	if len(matches) == 1 {
		return matches[0], false, nil
	}
	return "", reached && fromPerson == 0, nil
}

// Settle records the person's own check of an unknown write. The caller has the person's
// confirmation from the terminal.
func Settle(ctx context.Context, opts Options, id int64, sent bool) (Result, error) {
	w, err := Write(ctx, opts, id)
	if err != nil {
		return Result{}, err
	}
	if w.Outcome != string(da.Unknown) {
		return Result{}, errs.Usage("directed write " + strconv.FormatInt(id, 10) + " is already " + w.Outcome + "; only an unknown write is settled")
	}
	if w.Phase == "in_flight" && now().Sub(w.CreatedAt) < notSentAfter {
		return Result{}, errs.Usage("directed write " + strconv.FormatInt(id, 10) + " may still be in progress; settle it once it has been running for 10 minutes")
	}
	change := store.DirectedWriteChange{Outcome: string(da.Failed), Phase: "settled", Verification: "person", ErrorCode: w.ErrorCode, Cause: "person_not_sent"}
	if sent {
		change.Outcome, change.Cause = string(da.Sent), "person_sent"
	}
	var changed bool
	err = writeArchive(ctx, opts.DBPath, func(ctx context.Context, st *store.Store) (err error) {
		changed, err = st.UpdateDirectedWrite(ctx, w.ID, w.Outcome, w.Phase, change)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	if !changed {
		return Result{}, errs.Usage("directed write " + strconv.FormatInt(id, 10) + " changed while it was being settled; run the command again")
	}
	w.Outcome, w.Phase, w.Verification = change.Outcome, change.Phase, change.Verification
	return ofWrite(w), nil
}

// TargetResult is what a send to a chat, or a reply to a mail, needs to name.
type TargetResult struct {
	Chat        string   `json:"chat,omitempty"`
	Message     string   `json:"message,omitempty"`
	Identity    string   `json:"identity"`
	Known       bool     `json:"known"`
	Recipients  []string `json:"recipients"`
	Version     string   `json:"version,omitempty"`
	WatermarkAt string   `json:"watermark_at,omitempty"`
}

// Target reads a chat's or a mail's current audience and version, without writing anything. message,
// when set, is a mail's Internet Message-ID; otherwise chat is read.
func Target(ctx context.Context, opts Options, chat, message, identity string) (TargetResult, error) {
	if err := workIQEnabled(ctx, opts); err != nil {
		return TargetResult{}, err
	}
	s, p, err := openPinned(ctx, opts, identity)
	if err != nil {
		return TargetResult{}, err
	}
	defer func() { _ = s.Close() }()
	result := TargetResult{Identity: p.canonical, Recipients: []string{}}
	if message = strings.TrimSpace(message); message != "" {
		m, err := readMail(ctx, s, message, p)
		if err != nil {
			return TargetResult{}, err
		}
		result.Message, result.Known = message, m.known && m.kind == da.KindMailMessage
		if result.Known {
			result.Recipients, result.Version = m.recipients, m.version
		}
		return result, nil
	}
	result.Chat = strings.TrimSpace(chat)
	c, err := readChat(ctx, s, result.Chat, p)
	if err != nil {
		return TargetResult{}, err
	}
	result.Known, result.Version = c.known, c.version
	if c.known {
		result.Recipients = c.recipients
	}
	if !c.watermarkAt.IsZero() {
		result.WatermarkAt = c.watermarkAt.Format(time.RFC3339Nano)
	}
	return result, nil
}

// StatusResult is the switch and the newest writes, read offline.
type StatusResult struct {
	Writes    string     `json:"writes"`
	ChangedAt *time.Time `json:"changed_at"`
	Unknown   int        `json:"unknown"`
	Items     []LogItem  `json:"items"`
}

// LogItem is one write as status shows it.
type LogItem struct {
	RequestID    string    `json:"request_id"`
	CreatedAt    time.Time `json:"created_at"`
	Action       string    `json:"action"`
	Effect       string    `json:"effect"`
	Agent        string    `json:"agent"`
	Reason       string    `json:"reason"`
	Identity     string    `json:"identity"`
	Conversation string    `json:"conversation,omitempty"`
	Message      string    `json:"message,omitempty"`
	Recipients   []string  `json:"recipients"`
	Subject      string    `json:"subject,omitempty"`
	Text         string    `json:"text"`
	IntentKey    string    `json:"intent_key,omitempty"`
	Outcome      string    `json:"outcome"`
	Phase        string    `json:"phase"`
	Verification string    `json:"verification,omitempty"`
	ServerID     string    `json:"server_id,omitempty"`
	ErrorCode    string    `json:"error_code,omitempty"`
}

// Status reads the switch and the log without WorkIQ. An archive that does not exist has no writes.
func Status(ctx context.Context, opts Options, limit int) (StatusResult, error) {
	sw := ReadSwitch(opts.DBPath)
	result := StatusResult{Writes: "off", Items: []LogItem{}}
	if sw.On {
		result.Writes = "on"
	}
	if !sw.ChangedAt.IsZero() {
		result.ChangedAt = &sw.ChangedAt
	}
	var rows []store.DirectedWrite
	err := readArchive(ctx, opts.DBPath, func(s *store.Store) (err error) {
		all, err := s.DirectedWrites(ctx, 0)
		for _, w := range all {
			if w.Outcome == string(da.Unknown) {
				result.Unknown++
			}
		}
		rows = all[:min(limit, len(all))]
		return err
	})
	if err != nil && !errors.Is(err, store.ErrNoArchive) {
		return StatusResult{}, err
	}
	for _, w := range rows {
		result.Items = append(result.Items, LogItem{RequestID: strconv.FormatInt(w.ID, 10), CreatedAt: w.CreatedAt, Action: w.Action,
			Effect: w.Effect, Agent: w.Agent, Reason: w.Reason, Identity: w.Identity, Conversation: w.Conversation, Message: w.Message,
			Recipients: w.Recipients, Subject: w.Subject, Text: w.Text, IntentKey: w.IntentKey, Outcome: w.Outcome,
			Phase: w.Phase, Verification: w.Verification, ServerID: w.ServerID, ErrorCode: w.ErrorCode})
	}
	return result, nil
}
