package directedwrites

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	da "github.com/ourostack/m365crawl/internal/directedactions"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

const (
	SendTeamsMessage = "send_teams_message"
	SaveMailDraft    = "save_mail_draft"

	maxText    = 20000
	maxSubject = 255
	// notSentAfter is how long a Teams write must have been running before an absence of any newer
	// message from the person proves it was not sent.
	notSentAfter = 10 * time.Minute
	maxPages     = 5
)

// Options says which archive and WorkIQ invocation a write uses.
type Options struct {
	DBPath     string
	Invocation workiq.Invocation
	Version    string
}

// Result is the one object every write command prints.
type Result struct {
	Contract       string       `json:"contract"`
	Action         string       `json:"action"`
	Decision       da.Decision  `json:"decision,omitempty"`
	Code           da.Code      `json:"code,omitempty"`
	Field          string       `json:"field,omitempty"`
	Effect         da.Effect    `json:"effect,omitempty"`
	RequestID      string       `json:"request_id,omitempty"`
	ReplayOf       string       `json:"replay_of,omitempty"`
	EarlierOutcome da.Outcome   `json:"earlier_outcome,omitempty"`
	Outcome        da.Outcome   `json:"outcome,omitempty"`
	Phase          string       `json:"phase,omitempty"`
	Verification   string       `json:"verification,omitempty"`
	ServerID       string       `json:"server_id,omitempty"`
	ExitClass      da.ExitClass `json:"exit_class"`
	TellPerson     bool         `json:"tell_person"`
	Notice         string       `json:"notice,omitempty"`
}

// session is the WorkIQ write dispatcher as this package uses it.
type session interface {
	Fetch(ctx context.Context, path string) (workiq.Page, error)
	CreateChatMessage(ctx context.Context, chat, text string) workiq.WriteAnswer
	CreateMailDraft(ctx context.Context, subject, text string, to []string) workiq.WriteAnswer
	Close() error
}

// Test seams: the WorkIQ dispatcher and the archive.
var (
	openSession = func(ctx context.Context, inv workiq.Invocation, account string) (session, error) {
		return workiq.OpenWriter(ctx, inv, account)
	}
	readArchive  = readOnly
	writeArchive = store.WithWorkIQWriter
	// saveAttempts and savePause bound retrying the log after the server answered.
	saveAttempts = 20
	savePause    = 500 * time.Millisecond
)

func readOnly(ctx context.Context, path string, fn func(*store.Store) error) (err error) {
	s, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	return fn(s)
}

func coded(code string, exit int, message, fix string) *errs.Coded {
	return &errs.Coded{Code: code, Exit: exit, Message: message, Fix: fix}
}

func workIQDisabled() error {
	return coded("workiq_disabled", errs.ExitEnvironment, "the optional WorkIQ source is not enabled for this archive",
		"A person enables it once with `m365crawl workiq enable`; writes also need the person's `m365crawl writes on`.")
}

func identityChanged() error {
	return coded("workiq_identity_mismatch", errs.ExitEnvironment, "WorkIQ is signed in as a different account than the write",
		"Sign WorkIQ in as the account that made the write, then run the command again; nothing was changed.")
}

func chatAccountUnmatched() error {
	return coded("workiq_chat_account_unmatched", errs.ExitEnvironment, "WorkIQ's signed-in user is not a Teams account this archive reads",
		"Sync the Teams account first, or sign WorkIQ in as an account the archive already reads; nothing was sent.")
}

// classOf is the exit class of an accepted write's outcome.
func classOf(outcome da.Outcome) (da.ExitClass, bool) {
	switch outcome {
	case da.Sent, da.Queued:
		return da.ExitDone, false
	case da.Failed:
		return da.ExitFailed, false
	default:
		return da.ExitUnknown, true
	}
}

func decided(req da.Request, d da.Result) Result {
	return Result{Contract: da.Contract, Action: req.Action, Decision: d.Decision, Code: d.Code, Field: d.Field,
		Effect: d.Effect, ReplayOf: d.ReplayOf, EarlierOutcome: d.EarlierOutcome, ExitClass: d.ExitClass, TellPerson: d.TellPerson}
}

func invalid(req da.Request, field string) Result {
	return Result{Contract: da.Contract, Action: req.Action, Decision: da.Refuse, Code: da.CodeInvalidRequest, Field: field, ExitClass: da.ExitRequestProblem}
}

func ofWrite(w store.DirectedWrite) Result {
	class, tell := classOf(da.Outcome(w.Outcome))
	return Result{Contract: w.Contract, Action: w.Action, Effect: da.Effect(w.Effect), RequestID: strconv.FormatInt(w.ID, 10),
		Outcome: da.Outcome(w.Outcome), Phase: w.Phase, Verification: w.Verification, ServerID: w.ServerID, ExitClass: class, TellPerson: tell}
}

// normalized is content in the form that is sent and compared: LF line ends, no surrounding space.
func normalized(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(strings.ReplaceAll(s, "\r", "\n"))
}

func forbidden(r rune, line bool) bool {
	switch {
	case r == '\n' || r == '\t':
		return line
	case r < 0x20, r >= 0x7f && r <= 0x9f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r >= 0xe0000 && r <= 0xe007f:
		return true
	}
	return r == 0xfeff || r == 0x200b || r == 0x2060 || r == 0xad || r == 0x2028 || r == 0x2029
}

func clean(s string, line bool, limit int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > limit {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return forbidden(r, line) })
}

// checkInput applies m365crawl's plain-text rules to an accepted request.
func checkInput(req da.Request) (string, bool) {
	if !clean(normalized(req.Content.Text), false, maxText) {
		return "content.text", false
	}
	if !clean(normalized(req.Content.Subject), true, maxSubject) {
		return "content.subject", false
	}
	for _, r := range req.Target.Recipients {
		if !clean(strings.TrimSpace(r), true, 254) {
			return "target.recipients", false
		}
	}
	return "", true
}

// person is the signed-in account a write acts as.
type person struct {
	user      workiq.User
	canonical string // lower-case mail, else UPN
	pin       string // the --account WorkIQ was started with
}

func (p person) alias(address string) bool {
	address = strings.ToLower(strings.TrimSpace(address))
	return address != "" && (address == strings.ToLower(strings.TrimSpace(p.user.Mail)) || address == strings.ToLower(strings.TrimSpace(p.user.UPN)))
}

func me(ctx context.Context, s session) (workiq.User, error) {
	page, err := s.Fetch(ctx, "/me")
	if err != nil {
		return workiq.User{}, err
	}
	return workiq.DecodeUser(ctx, page.Data)
}

func openAs(ctx context.Context, opts Options, account string) (session, workiq.User, error) {
	s, err := openSession(ctx, opts.Invocation, account)
	if err != nil {
		return nil, workiq.User{}, err
	}
	user, err := me(ctx, s)
	if err != nil {
		return nil, workiq.User{}, errors.Join(err, s.Close())
	}
	return s, user, nil
}

// openPinned starts WorkIQ pinned to identity, or, without one, reads /me unpinned and reopens
// pinned to that user's canonical address with the same user id.
func openPinned(ctx context.Context, opts Options, identity string) (session, person, error) {
	pin := strings.TrimSpace(identity)
	if pin == "" {
		s, user, err := openAs(ctx, opts, "")
		if err != nil {
			return nil, person{}, err
		}
		if err := s.Close(); err != nil {
			return nil, person{}, err
		}
		binding, err := user.DiscoveryBinding()
		if err != nil {
			return nil, person{}, err
		}
		pin = binding.Address
		s, again, err := openAs(ctx, opts, pin)
		if err != nil {
			return nil, person{}, err
		}
		if again.ID != user.ID {
			return nil, person{}, errors.Join(identityChanged(), s.Close())
		}
		return s, person{user: again, canonical: pin, pin: pin}, nil
	}
	s, user, err := openAs(ctx, opts, pin)
	if err != nil {
		return nil, person{}, err
	}
	binding, err := user.DiscoveryBinding()
	if err != nil {
		return nil, person{}, errors.Join(err, s.Close())
	}
	return s, person{user: user, canonical: binding.Address, pin: pin}, nil
}

// chat is what a send knows about its conversation.
type chat struct {
	known       bool
	recipients  []string
	version     string
	watermarkID string
	watermarkAt time.Time
}

// gone says a read answered that the target is not there or not readable as a chat.
func gone(page workiq.Page, err error) bool {
	return err != nil && page.StatusCode >= 400 && page.StatusCode < 500
}

func readChat(ctx context.Context, s session, id string, p person) (chat, error) {
	if !workiq.ValidChatID(id) {
		return chat{}, nil
	}
	recipients := []string{p.canonical}
	if id != workiq.NotesToSelf {
		page, err := s.Fetch(ctx, "/chats/"+id+"?$select=id,chatType")
		if gone(page, err) {
			return chat{}, nil
		}
		if err != nil {
			return chat{}, err
		}
		var info struct{ ChatType string }
		_ = json.Unmarshal(page.Data, &info)
		page, err = s.Fetch(ctx, "/chats/"+id+"/members")
		if gone(page, err) {
			return chat{}, nil
		}
		if err != nil {
			return chat{}, err
		}
		var members struct {
			Value []struct{ Email, UserID string }
		}
		if json.Unmarshal(page.Data, &members) != nil || page.NextCursor != "" {
			return chat{}, nil
		}
		recipients = nil
		self := false
		for _, m := range members.Value {
			key := strings.ToLower(strings.TrimSpace(m.Email))
			if m.UserID == p.user.ID {
				key, self = p.canonical, true
			}
			if key == "" {
				key = strings.ToLower(strings.TrimSpace(m.UserID))
			}
			recipients = append(recipients, key)
		}
		if !self || len(recipients) < 2 || info.ChatType == "oneOnOne" && len(recipients) != 2 {
			return chat{}, nil
		}
		c, err := newest(ctx, s, id)
		c.recipients = recipients
		return c, err
	}
	c, err := newest(ctx, s, id)
	c.recipients = recipients
	return c, err
}

// newest reads the chat's newest message: its id is the chat's version and, with its time, the
// write's watermark.
func newest(ctx context.Context, s session, id string) (chat, error) {
	page, err := s.Fetch(ctx, "/chats/"+id+"/messages?$top=1&$orderby=createdDateTime%20desc")
	if gone(page, err) {
		return chat{}, nil
	}
	if err != nil {
		return chat{}, err
	}
	var list struct{ Value []json.RawMessage }
	if json.Unmarshal(page.Data, &list) != nil {
		return chat{}, nil
	}
	c := chat{known: true, version: "none"}
	if len(list.Value) > 0 {
		m, err := workiq.DecodeWrittenMessage(ctx, list.Value[0])
		if err != nil {
			return chat{}, err
		}
		c.version, c.watermarkID, c.watermarkAt = m.ID, m.ID, m.Created
	}
	return c, nil
}

func earlierOf(rows []store.DirectedWrite) []da.Earlier {
	out := make([]da.Earlier, 0, len(rows))
	for _, w := range rows {
		out = append(out, da.Earlier{RequestID: strconv.FormatInt(w.ID, 10), Outcome: da.Outcome(w.Outcome), Request: da.Request{
			Contract: w.Contract, Action: w.Action, Agent: w.Agent, Reason: w.Reason, Identity: w.Identity,
			Target:  da.Target{Conversation: w.Conversation, Message: w.Message, Recipients: w.Recipients},
			Content: da.Content{Text: w.Text, Subject: w.Subject}, ExpectedVersion: w.ExpectedVersion, IntentKey: w.IntentKey,
		}})
	}
	return out
}

// canonicalize makes the person's own address compare as one form everywhere.
func canonicalize(req da.Request, p person) da.Request {
	if p.alias(req.Identity) {
		req.Identity = p.canonical
	}
	recipients := make([]string, len(req.Target.Recipients))
	for i, r := range req.Target.Recipients {
		recipients[i] = r
		if p.alias(r) {
			recipients[i] = p.canonical
		}
	}
	req.Target.Recipients = recipients
	return req
}

func teamsAccount(ctx context.Context, opts Options, p person) (string, error) {
	tenant, found := "", false
	err := readArchive(ctx, opts.DBPath, func(s *store.Store) error {
		facts, err := s.WorkIQAccounts(ctx)
		for _, fact := range facts {
			if fact.Family == "chats" && fact.UserID == p.user.ID {
				tenant, found = fact.TenantID, true
			}
		}
		return err
	})
	if err != nil {
		return "", err
	}
	if !found {
		return "", chatAccountUnmatched()
	}
	return tenant, nil
}

func workIQEnabled(ctx context.Context, opts Options) error {
	enabled := false
	err := readArchive(ctx, opts.DBPath, func(s *store.Store) (err error) {
		enabled, err = s.WorkIQEnabled(ctx)
		return err
	})
	if errors.Is(err, store.ErrNoArchive) || err == nil && !enabled {
		return workIQDisabled()
	}
	return err
}

func earlierWrites(ctx context.Context, opts Options) ([]store.DirectedWrite, error) {
	var rows []store.DirectedWrite
	err := readArchive(ctx, opts.DBPath, func(s *store.Store) (err error) {
		rows, err = s.DirectedWrites(ctx, 0)
		return err
	})
	return rows, err
}

var errGate = errors.New("the decision changed before the write began")

// Send carries out one directed send or draft. An error means nothing was sent and no decision
// was made (WorkIQ or the archive was not ready); every decision is a Result.
func Send(ctx context.Context, opts Options, req da.Request) (Result, error) {
	writes := "off"
	if ReadSwitch(opts.DBPath).On {
		writes = "on"
	}
	offline := da.Decide(da.State{Writes: writes, SignedIn: req.Identity}, req)
	if offline.Decision == da.Refuse && offline.Code != da.CodeUnknownTarget {
		return decided(req, offline), nil
	}
	if field, ok := checkInput(req); !ok {
		return invalid(req, field), nil
	}
	if err := workIQEnabled(ctx, opts); err != nil {
		return Result{}, err
	}
	s, p, err := openPinned(ctx, opts, req.Identity)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = s.Close() }()
	req = canonicalize(req, p)
	state := da.State{Writes: writes, SignedIn: p.canonical, Targets: map[string]da.TargetState{}}
	tenant := ""
	var target chat
	if req.Action == SendTeamsMessage {
		if tenant, err = teamsAccount(ctx, opts, p); err != nil {
			return Result{}, err
		}
		conversation := strings.TrimSpace(req.Target.Conversation)
		if target, err = readChat(ctx, s, conversation, p); err != nil {
			return Result{}, err
		}
		if target.known {
			state.Targets[conversation] = da.TargetState{Kind: da.KindConversation, Recipients: target.recipients, Version: target.version}
		}
	}
	rows, err := earlierWrites(ctx, opts)
	if err != nil {
		return Result{}, err
	}
	state.Earlier = earlierOf(rows)
	if d := da.Decide(state, req); d.Decision != da.Accept {
		return decided(req, d), nil
	}
	effect, _ := da.EffectOf(req.Action)
	text := normalized(req.Content.Text)
	row := store.DirectedWrite{Contract: da.Contract, Action: req.Action, Effect: string(effect), Agent: strings.TrimSpace(req.Agent),
		Reason: strings.TrimSpace(req.Reason), Identity: p.canonical, Conversation: strings.TrimSpace(req.Target.Conversation),
		Recipients: lower(req.Target.Recipients), Subject: normalized(req.Content.Subject), Text: text,
		IntentKey: strings.TrimSpace(req.IntentKey), ExpectedVersion: strings.TrimSpace(req.ExpectedVersion), UserID: p.user.ID,
		TenantID: tenant, AccountPin: p.pin, Outcome: string(da.Unknown), Phase: "in_flight", WatermarkID: target.watermarkID,
		Version: opts.Version}
	if !target.watermarkAt.IsZero() {
		row.WatermarkAt = target.watermarkAt.Format(time.RFC3339Nano)
	}
	var changed da.Result
	err = writeArchive(ctx, opts.DBPath, func(ctx context.Context, st *store.Store) error {
		var err error
		row.ID, err = st.BeginDirectedWrite(ctx, row, func(committed []store.DirectedWrite) error {
			again := state
			again.Writes = "off"
			if ReadSwitch(opts.DBPath).On {
				again.Writes = "on"
			}
			again.Earlier = earlierOf(committed)
			if changed = da.Decide(again, req); changed.Decision != da.Accept {
				return errGate
			}
			return nil
		})
		return err
	})
	if errors.Is(err, errGate) {
		return decided(req, changed), nil
	}
	if err != nil {
		return Result{}, err
	}
	var answer workiq.WriteAnswer
	if req.Action == SendTeamsMessage {
		answer = s.CreateChatMessage(ctx, row.Conversation, text)
	} else {
		answer = s.CreateMailDraft(ctx, row.Subject, text, row.Recipients)
	}
	change := store.DirectedWriteChange{Outcome: string(da.Unknown), Phase: "answered", ServerStatus: answer.Status, ErrorCode: answer.Code, Cause: "server_" + string(answer.Kind)}
	switch {
	case answer.Kind == workiq.WriteRejected:
		change.Outcome = string(da.Failed)
	case answer.Kind == workiq.WriteCreated && createdAsAsked(row, answer, p):
		change.ServerID = answer.ServerID
		row.ServerID = answer.ServerID
		if found, _ := verifyByID(ctx, s, row, p.user.ID); found {
			change.Outcome, change.Phase, change.Verification, change.Cause = string(da.Sent), "verified", "found", "verified_by_read"
		}
	case answer.Kind == workiq.WriteCreated:
		change.ServerID, change.Cause = answer.ServerID, "server_created_elsewhere"
	}
	return save(ctx, opts, row, "in_flight", change), nil
}

// createdAsAsked says a created answer names the requested chat and the signed-in sender, or is a draft.
func createdAsAsked(row store.DirectedWrite, answer workiq.WriteAnswer, p person) bool {
	if row.Action == SaveMailDraft {
		return answer.IsDraft
	}
	return answer.ChatID == row.Conversation && answer.FromUserID == p.user.ID
}

func lower(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return out
}

// save records the answer, retrying a busy archive. The printed outcome is the answer's even when
// the log cannot be saved: the log then stays unknown and writes verify repairs it.
func save(ctx context.Context, opts Options, row store.DirectedWrite, fromPhase string, change store.DirectedWriteChange) Result {
	row.Outcome, row.Phase, row.Verification = change.Outcome, change.Phase, change.Verification
	if change.ServerID != "" {
		row.ServerID = change.ServerID
	}
	result := ofWrite(row)
	var err error
	for attempt := 0; attempt < saveAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(savePause)
		}
		err = writeArchive(context.WithoutCancel(ctx), opts.DBPath, func(ctx context.Context, st *store.Store) error {
			_, err := st.UpdateDirectedWrite(ctx, row.ID, string(da.Unknown), fromPhase, change)
			return err
		})
		if err == nil {
			return result
		}
	}
	result.Notice = "the write log could not be saved; it still says unknown until `writes verify " + result.RequestID + "` reads the server again"
	return result
}

// sameText compares what was written with what the server returns, after the same scrub and with
// white-space runs collapsed.
func sameText(written, read string) bool {
	return strings.Join(strings.Fields(workiq.ScrubText(written)), " ") == strings.Join(strings.Fields(read), " ")
}

func verifyByID(ctx context.Context, s session, row store.DirectedWrite, userID string) (bool, error) {
	path := "/me/messages/" + url.PathEscape(row.ServerID) + "?$select=id,isDraft,subject,body,toRecipients"
	if row.Action == SendTeamsMessage {
		path = "/chats/" + row.Conversation + "/messages/" + url.PathEscape(row.ServerID)
	}
	page, err := s.Fetch(ctx, path)
	if err != nil {
		return false, err
	}
	m, err := workiq.DecodeWrittenMessage(ctx, page.Data)
	if err != nil {
		return false, err
	}
	if row.Action == SaveMailDraft {
		return m.IsDraft && sameText(row.Subject, m.Subject) && sameText(row.Text, m.Text) && sameSet(row.Recipients, m.Recipients), nil
	}
	return m.FromUserID == userID && !m.Deleted && (m.ChatID == "" || m.ChatID == row.Conversation) && sameText(row.Text, m.Text), nil
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}
