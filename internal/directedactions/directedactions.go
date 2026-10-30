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
	Recipients []string `json:"recipients"`
	Version    string   `json:"version"`
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
		CodeRecipientsChanged, CodeStaleVersion, CodeOutcomeUnknown,
	}
}

type action struct {
	effect   Effect // empty for the person's writes switch
	send     bool
	required []string
}

var actions = map[string]action{
	"send_teams_message": {VisibleToOthers, true, []string{"target.conversation", "target.recipients", "content.text"}},
	"send_mail":          {VisibleToOthers, true, []string{"target.recipients", "content.subject", "content.text"}},
	"reply_mail":         {VisibleToOthers, true, []string{"target.message", "target.recipients", "content.text"}},
	"add_reaction":       {VisibleToOthers, false, []string{"target.conversation", "target.message", "target.recipients", "content.reaction"}},
	"remove_reaction":    {VisibleToOthers, false, []string{"target.conversation", "target.message", "target.recipients", "content.reaction"}},
	"save_mail_draft":    {OnlyYou, false, []string{"content.text"}},
	"discard_mail_draft": {OnlyYou, false, []string{"target.message"}},
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

func present(r Request, field string) bool {
	switch field {
	case "target.conversation":
		return !blank(r.Target.Conversation)
	case "target.message":
		return !blank(r.Target.Message)
	case "target.recipients":
		return len(r.Target.Recipients) > 0 && !slices.ContainsFunc(r.Target.Recipients, blank)
	case "content.text":
		return !blank(r.Content.Text)
	case "content.subject":
		return !blank(r.Content.Subject)
	default: // content.reaction
		return !blank(r.Content.Reaction)
	}
}

func targetKey(t Target) string {
	if t.Conversation != "" {
		return t.Conversation
	}
	return t.Message
}

func sameAudience(a, b []string) bool {
	norm := func(list []string) []string {
		out := make([]string, len(list))
		for i, s := range list {
			out[i] = strings.ToLower(strings.TrimSpace(s))
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return slices.Equal(norm(a), norm(b))
}

// sameRequest compares what an action does, not who asked or why, so a retry may reword its reason.
func sameRequest(a, b Request) bool {
	return a.Action == b.Action &&
		strings.EqualFold(a.Identity, b.Identity) &&
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
	if req.IntentKey != "" && !a.send {
		return refuse(CodeIntentKeyNotAllowed)
	}
	for _, field := range a.required {
		if !present(req, field) {
			return invalid(field)
		}
	}
	key := targetKey(req.Target)
	if req.ExpectedVersion != "" && key == "" {
		return invalid("expected_version")
	}
	if a.effect == VisibleToOthers && blank(req.Identity) {
		return refuse(CodeIdentityRequired)
	}
	if !blank(req.Identity) && !strings.EqualFold(strings.TrimSpace(req.Identity), strings.TrimSpace(state.SignedIn)) {
		return refuse(CodeIdentityMismatch)
	}
	if req.IntentKey != "" {
		for _, e := range state.Earlier {
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
	if key != "" {
		current, ok := state.Targets[key]
		if !ok {
			return refuse(CodeUnknownTarget)
		}
		if a.effect == VisibleToOthers && !sameAudience(req.Target.Recipients, current.Recipients) {
			return refuse(CodeRecipientsChanged)
		}
		if req.ExpectedVersion != "" && req.ExpectedVersion != current.Version {
			return refuse(CodeStaleVersion)
		}
	}
	for _, e := range state.Earlier {
		if e.Outcome == Unknown && sameRequest(e.Request, req) {
			return refuse(CodeOutcomeUnknown)
		}
	}
	return Result{Decision: Accept, Effect: a.effect}
}
