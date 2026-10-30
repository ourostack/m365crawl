// Package directedactions decides whether a tool may act on an agent's directed request, following
// contracts/directed-actions/v0/contract.md. It is pure: it reads the request and what the tool
// knows, and never acts.
package directedactions

import (
	"slices"
	"strings"
)

const Contract = "directed-actions/v0"

type (
	Effect    string
	Decision  string
	Code      string
	Outcome   string
	ExitClass string
)

const (
	OnlyYou         Effect = "only_you"
	VisibleToOthers Effect = "visible_to_others"

	Accept Decision = "accept"
	Refuse Decision = "refuse"
	Replay Decision = "replay"

	Queued  Outcome = "queued"
	Sent    Outcome = "sent"
	Failed  Outcome = "failed"
	Unknown Outcome = "unknown"

	ExitDone           ExitClass = "done"
	ExitRequestProblem ExitClass = "request_problem"
	ExitPersonNeeded   ExitClass = "person_needed"
	ExitFailed         ExitClass = "failed"
	ExitUnknown        ExitClass = "unknown"
)

type Request struct {
	Contract        string  `json:"contract"`
	Action          string  `json:"action"`
	Agent           string  `json:"agent"`
	Reason          string  `json:"reason"`
	Identity        string  `json:"identity,omitempty"`
	Target          Target  `json:"target,omitzero"`
	Content         Content `json:"content,omitzero"`
	ExpectedVersion string  `json:"expected_version,omitempty"`
	IntentKey       string  `json:"intent_key,omitempty"`
}

type Target struct {
	Conversation string   `json:"conversation,omitempty"`
	Message      string   `json:"message,omitempty"`
	Recipients   []string `json:"recipients,omitempty"`
}

type Content struct {
	Text     string `json:"text,omitempty"`
	Subject  string `json:"subject,omitempty"`
	Reaction string `json:"reaction,omitempty"`
}

type State struct {
	Writes   string                 `json:"writes"`
	SignedIn string                 `json:"signed_in"`
	Targets  map[string]TargetState `json:"targets"`
	Earlier  []Earlier              `json:"earlier"`
}

type TargetState struct {
	Conversation string   `json:"conversation,omitempty"` // for a message: the conversation it belongs to
	Recipients   []string `json:"recipients"`
	Version      string   `json:"version"`
}

type Earlier struct {
	RequestID string  `json:"request_id"`
	Outcome   Outcome `json:"outcome"`
	Request   Request `json:"request"`
}

type Result struct {
	Decision       Decision  `json:"decision"`
	Effect         Effect    `json:"effect,omitempty"`
	Code           Code      `json:"code,omitempty"`
	Field          string    `json:"field,omitempty"`
	ReplayOf       string    `json:"replay_of,omitempty"`
	EarlierOutcome Outcome   `json:"earlier_outcome,omitempty"`
	ExitClass      ExitClass `json:"exit_class,omitempty"`
	TellPerson     bool      `json:"tell_person,omitempty"`
}

const (
	CodeUnsupportedContract Code = "unsupported_contract"
	CodeUnknownAction       Code = "unknown_action"
	CodeMissingAgent        Code = "missing_agent"
	CodeMissingReason       Code = "missing_reason"
	CodePersonOnly          Code = "person_only"
	CodeWritesOff           Code = "writes_off"
	CodeIntentKeyNotAllowed Code = "intent_key_not_allowed"
	CodeInvalidRequest      Code = "invalid_request"
	CodeIdentityRequired    Code = "identity_required"
	CodeIdentityMismatch    Code = "identity_mismatch"
	CodeIntentKeyReused     Code = "intent_key_reused"
	CodeUnknownTarget       Code = "unknown_target"
	CodeTargetMismatch      Code = "target_mismatch"
	CodeRecipientsChanged   Code = "recipients_changed"
	CodeStaleVersion        Code = "stale_version"
	CodeOutcomeUnknown      Code = "outcome_unknown"
)

// Codes lists every refusal code in the order the decision checks them.
func Codes() []Code {
	return []Code{
		CodeUnsupportedContract, CodeUnknownAction, CodeMissingAgent, CodeMissingReason,
		CodePersonOnly, CodeWritesOff, CodeIntentKeyNotAllowed, CodeInvalidRequest,
		CodeIdentityRequired, CodeIdentityMismatch, CodeIntentKeyReused, CodeUnknownTarget,
		CodeTargetMismatch, CodeRecipientsChanged, CodeStaleVersion, CodeOutcomeUnknown,
	}
}

const (
	fConversation = "target.conversation"
	fMessage      = "target.message"
	fRecipients   = "target.recipients"
	fText         = "content.text"
	fSubject      = "content.subject"
	fReaction     = "content.reaction"
	fVersion      = "expected_version"
)

// fieldOrder is the order in which missing and unexpected fields are reported.
var fieldOrder = []string{fConversation, fMessage, fRecipients, fText, fSubject, fReaction, fVersion}

type lookup int

const (
	lookupNone lookup = iota
	lookupConversation
	lookupMessage
)

type action struct {
	effect   Effect // empty for the person's writes switch
	send     bool
	required []string
	optional []string
	lookup   lookup // the target that holds the audience and version
}

var actions = map[string]action{
	"send_teams_message": {VisibleToOthers, true, []string{fConversation, fRecipients, fText}, []string{fVersion}, lookupConversation},
	"send_mail":          {VisibleToOthers, true, []string{fRecipients, fSubject, fText}, nil, lookupNone},
	"reply_mail":         {VisibleToOthers, true, []string{fMessage, fRecipients, fText}, []string{fVersion}, lookupMessage},
	"add_reaction":       {VisibleToOthers, false, []string{fConversation, fMessage, fRecipients, fReaction}, []string{fVersion}, lookupMessage},
	"remove_reaction":    {VisibleToOthers, false, []string{fConversation, fMessage, fRecipients, fReaction}, []string{fVersion}, lookupMessage},
	"save_mail_draft":    {OnlyYou, false, []string{fText}, []string{fMessage, fRecipients, fSubject, fVersion}, lookupMessage},
	"discard_mail_draft": {OnlyYou, false, []string{fMessage}, []string{fVersion}, lookupMessage},
	"turn_writes_on":     {},
	"turn_writes_off":    {},
}

// Actions lists every action name the contract defines, sorted.
func Actions() []string {
	names := make([]string, 0, len(actions))
	for name := range actions {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// EffectOf reports an action's effect class. The writes switch and unknown actions have none.
func EffectOf(name string) (Effect, bool) {
	a := actions[name]
	return a.effect, a.effect != ""
}

func refuse(code Code) Result {
	r := Result{Decision: Refuse, Code: code, ExitClass: ExitRequestProblem}
	switch code {
	case CodePersonOnly, CodeWritesOff, CodeOutcomeUnknown:
		r.ExitClass, r.TellPerson = ExitPersonNeeded, true
	}
	return r
}

func invalid(field string) Result {
	r := refuse(CodeInvalidRequest)
	r.Field = field
	return r
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

// account is how identities and recipients compare everywhere: without surrounding white space
// and ignoring letter case.
func account(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func accounts(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = account(s)
	}
	return out
}

// normalize applies the contract's comparison rules once, so every check and every comparison
// with an earlier request sees the same values.
func normalize(r Request) Request {
	r.Identity = account(r.Identity)
	r.Target.Conversation = strings.TrimSpace(r.Target.Conversation)
	r.Target.Message = strings.TrimSpace(r.Target.Message)
	r.Target.Recipients = accounts(r.Target.Recipients)
	r.Content.Text = strings.TrimSpace(r.Content.Text)
	r.Content.Subject = strings.TrimSpace(r.Content.Subject)
	r.Content.Reaction = strings.TrimSpace(r.Content.Reaction)
	r.ExpectedVersion = strings.TrimSpace(r.ExpectedVersion)
	r.IntentKey = strings.TrimSpace(r.IntentKey)
	return r
}

func given(r Request, field string) bool {
	switch field {
	case fConversation:
		return r.Target.Conversation != ""
	case fMessage:
		return r.Target.Message != ""
	case fRecipients:
		return len(r.Target.Recipients) > 0
	case fText:
		return r.Content.Text != ""
	case fSubject:
		return r.Content.Subject != ""
	case fReaction:
		return r.Content.Reaction != ""
	default: // expected_version
		return r.ExpectedVersion != ""
	}
}

func (a action) key(r Request) string {
	switch a.lookup {
	case lookupConversation:
		return r.Target.Conversation
	case lookupMessage:
		return r.Target.Message
	default:
		return ""
	}
}

// checkFields reports the first missing required field, then the first field the action does not
// use, so an unused field can never make a request look different from an earlier one.
func (a action) checkFields(r Request) (string, bool) {
	for _, f := range a.required {
		if !given(r, f) {
			return f, false
		}
	}
	for _, f := range fieldOrder {
		if given(r, f) && !slices.Contains(a.required, f) && !slices.Contains(a.optional, f) {
			return f, false
		}
	}
	if slices.Contains(r.Target.Recipients, "") {
		return fRecipients, false
	}
	if given(r, fVersion) && a.key(r) == "" {
		return fVersion, false
	}
	return "", true
}

func sameAudience(a, b []string) bool {
	set := func(list []string) []string {
		out := slices.Clone(list)
		slices.Sort(out)
		return slices.Compact(out)
	}
	return slices.Equal(set(a), set(b))
}

// sameRequest compares two normalized requests by what the action does, not who asked or why,
// so a retry may reword its reason.
func sameRequest(a, b Request) bool {
	return a.Action == b.Action &&
		a.Identity == b.Identity &&
		a.Target.Conversation == b.Target.Conversation &&
		a.Target.Message == b.Target.Message &&
		sameAudience(a.Target.Recipients, b.Target.Recipients) &&
		a.Content == b.Content
}

// Decide applies the contract's checks in order and returns the first refusal, a replay of an
// earlier request with the same intent key, or acceptance.
func Decide(state State, req Request) Result {
	if req.Contract != Contract {
		return refuse(CodeUnsupportedContract)
	}
	a, known := actions[req.Action]
	if !known {
		return refuse(CodeUnknownAction)
	}
	if blank(req.Agent) {
		return refuse(CodeMissingAgent)
	}
	if blank(req.Reason) {
		return refuse(CodeMissingReason)
	}
	if a.effect == "" {
		return refuse(CodePersonOnly)
	}
	if state.Writes != "on" {
		return refuse(CodeWritesOff)
	}
	req = normalize(req)
	if req.IntentKey != "" && !a.send {
		return refuse(CodeIntentKeyNotAllowed)
	}
	if field, ok := a.checkFields(req); !ok {
		return invalid(field)
	}
	if a.effect == VisibleToOthers && req.Identity == "" {
		return refuse(CodeIdentityRequired)
	}
	if req.Identity != "" && req.Identity != account(state.SignedIn) {
		return refuse(CodeIdentityMismatch)
	}
	earlier := make([]Earlier, len(state.Earlier))
	for i, e := range state.Earlier {
		e.Request = normalize(e.Request)
		earlier[i] = e
	}
	if req.IntentKey != "" {
		for _, e := range earlier {
			if e.Request.IntentKey != req.IntentKey || e.Outcome == Failed {
				continue
			}
			if !sameRequest(e.Request, req) {
				return refuse(CodeIntentKeyReused)
			}
			r := Result{Decision: Replay, ReplayOf: e.RequestID, EarlierOutcome: e.Outcome, ExitClass: ExitDone}
			if e.Outcome == Unknown {
				r.ExitClass, r.TellPerson = ExitUnknown, true
			}
			return r
		}
	}
	if c := req.Target.Conversation; c != "" {
		if _, ok := state.Targets[c]; !ok {
			return refuse(CodeUnknownTarget)
		}
	}
	if key := a.key(req); key != "" {
		current, ok := state.Targets[key]
		if !ok {
			return refuse(CodeUnknownTarget)
		}
		if a.lookup == lookupMessage && req.Target.Conversation != "" && current.Conversation != req.Target.Conversation {
			return refuse(CodeTargetMismatch)
		}
		if a.effect == VisibleToOthers && !sameAudience(req.Target.Recipients, accounts(current.Recipients)) {
			return refuse(CodeRecipientsChanged)
		}
		if req.ExpectedVersion != "" && req.ExpectedVersion != current.Version {
			return refuse(CodeStaleVersion)
		}
	}
	for _, e := range earlier {
		if e.Outcome == Unknown && sameRequest(e.Request, req) {
			return refuse(CodeOutcomeUnknown)
		}
	}
	return Result{Decision: Accept, Effect: a.effect}
}
