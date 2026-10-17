package teamsdrafts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

const DecoderQualification = "teams-draft-decoder-v1"
const ProjectorQualification = "teams-html-to-text-v1"

type Presence struct {
	State string  `json:"state"`
	Value *string `json:"value"`
}
type EditClock struct {
	State        string `json:"state"`
	Milliseconds *int64 `json:"milliseconds"`
}
type Envelope struct {
	Source, TenantID, UserID, Database, Store, KeyJSON string
	FirstSeenAt, UpdatedAt                             string
	RemovedAt                                          *string
}
type TargetClaim struct {
	SourceThreadID     Presence `json:"source_thread_id"`
	SourceMessageID    Presence `json:"source_message_id"`
	ClientMessageID    Presence `json:"client_message_id"`
	SourceReplyChainID Presence `json:"source_reply_chain_id"`
}
type Body struct {
	ID, BodyRevision                                                 string
	Envelope                                                         Envelope
	WrapperConversationID, WrapperReplyChainID, MemberKey, MessageID string
	ClientDraftID                                                    Presence
	BodyState                                                        string
	HTML, Text                                                       *string
	LastEdit                                                         EditClock
	VersioningTimestamp                                              Presence
	Claim                                                            TargetClaim
}
type Diagnostic struct {
	Code     string `json:"code"`
	Field    string `json:"field"`
	Category string `json:"category"`
	Count    int    `json:"count"`
}
type WrapperResult struct {
	Bodies         []Body
	Diagnostics    []Diagnostic
	VisitedMembers int
	Complete       bool
}
type Metadata struct {
	Claim            TargetClaim
	ConversationType Presence
}

var origin = regexp.MustCompile(`^https_teams\.(microsoft\.com|cloud\.microsoft)_[0-9]+$`)

func EligibleSource(source string) bool {
	p, o, ok := strings.Cut(source, "|")
	return ok && p != "" && p != "." && p != ".." && !strings.ContainsAny(p, "/\\\x00") && origin.MatchString(o)
}

// validateObject rejects duplicate namespaces before any member can acquire an ID.
func validateObject(raw []byte) (map[string]any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := uniqueValue(d); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v map[string]any
	if err := d.Decode(&v); err != nil || v == nil {
		return nil, errors.New("expected object")
	}
	return v, nil
}

func uniqueValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return errors.New("unexpected delimiter")
	}
	keys := map[string]bool{}
	for d.More() {
		if delim == '{' {
			t, err = d.Token()
			if err != nil {
				return err
			}
			k, ok := t.(string)
			if !ok || keys[k] {
				return errors.New("duplicate object member")
			}
			keys[k] = true
		}
		if err := uniqueValue(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func stringOf(v any) string       { s, _ := v.(string); return s }
func undefined(v any) bool {
	m, ok := v.(map[string]any)
	return ok && len(m) == 1 && m["$undefined"] == true
}
func presence(m map[string]any, k string) Presence {
	v, ok := m[k]
	if !ok {
		return Presence{State: "missing"}
	}
	if v == nil {
		return Presence{State: "null"}
	}
	if undefined(v) {
		return Presence{State: "undefined"}
	}
	if s, ok := v.(string); ok {
		return Presence{State: "value", Value: &s}
	}
	return Presence{State: "invalid"}
}
func claim(m map[string]any) TargetClaim {
	return TargetClaim{presence(m, "sourceThreadId"), presence(m, "sourceMessageId"), presence(m, "clientMessageId"), presence(m, "sourceReplyChainId")}
}
func editClock(m map[string]any) EditClock {
	p := presence(m, "lastEditTimestamp")
	n, ok := m["lastEditTimestamp"].(json.Number)
	if !ok {
		if p.State == "value" {
			p.State = "invalid"
		}
		return EditClock{State: p.State}
	}
	x, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil {
		return EditClock{State: "invalid"}
	}
	return EditClock{State: "value", Milliseconds: &x}
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func representationCategory(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return "missing"
	}
	if undefined(v) {
		return "undefined"
	}
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	default:
		return "object"
	}
}
func guardDiagnostic(field string, m, p, a, c map[string]any) Diagnostic {
	prefix, key, _ := strings.Cut(field, ".")
	obj := m
	switch prefix {
	case "properties":
		obj = p
	case "activity":
		obj = a
	case "clientDraftProperties":
		obj = c
	}
	category := representationCategory(obj, key)
	code := "unsupported_guard_representation"
	switch field {
	case "message.creator", "activity.sourceUserId":
		code = "ownership_unqualified"
	case "message.type", "message.draftType", "message.threadType", "message.messageType", "message.contentType", "message.state", "message.isSentByCurrentUser", "activity.activityType", "activity.activitySubtype":
		code = "unsupported_discriminator"
		if category == "missing" {
			code = "missing_discriminator"
		}
	}
	return Diagnostic{code, field, category, 1}
}

func DecodeWrapper(ctx context.Context, e Envelope, raw []byte, memberBudget int) (WrapperResult, error) {
	out := WrapperResult{Complete: true}
	if err := ctx.Err(); err != nil {
		return WrapperResult{}, err
	}
	manager, a, ok := teamsdesktop.ParseDatabaseName(e.Database)
	if !EligibleSource(e.Source) || !ok || manager != "streams-replychain-manager" || e.Store != "drafts" || a.TenantID != e.TenantID || a.UserID != e.UserID {
		return out, nil
	}
	w, err := validateObject(raw)
	var key []string
	if err != nil || !utf8.ValidString(e.KeyJSON) || json.Unmarshal([]byte(e.KeyJSON), &key) != nil || len(key) != 2 || key[0] == "" || key[1] == "" || key[0] != stringOf(w["conversationId"]) || key[1] != stringOf(w["replyChainId"]) {
		return WrapperResult{}, nil
	}
	members := object(w["messageMap"])
	if members == nil {
		return WrapperResult{}, nil
	}
	names := make([]string, 0, len(members))
	for k := range members {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if err := ctx.Err(); err != nil {
			return WrapperResult{}, err
		}
		if out.VisitedMembers >= memberBudget {
			out.Complete = false
			out.Bodies = nil
			return out, nil
		}
		out.VisitedMembers++
		if k == "" || stringOf(object(members[k])["id"]) == "" {
			out.Complete = false
			out.Bodies = nil
			return out, nil
		}
		m := object(members[k])
		p := object(m["properties"])
		a := object(p["activity"])
		c := object(object(m["draftDetails"])["clientDraftProperties"])
		field := ""
		if stringOf(m["creator"]) != "8:orgid:"+e.UserID {
			field = "message.creator"
		} else if stringOf(a["sourceUserId"]) != "8:orgid:"+e.UserID {
			field = "activity.sourceUserId"
		}
		for _, g := range []struct {
			obj    map[string]any
			prefix string
			fields [][2]string
		}{
			{m, "message.", [][2]string{{"type", "Message"}, {"draftType", "RegularDraft"}, {"threadType", "streamofdrafts"}, {"messageType", "RichText/Html"}, {"contentType", "Text"}, {"lieType", "Send"}}},
			{a, "activity.", [][2]string{{"activityType", "Draft"}, {"activitySubtype", "Draft"}}},
		} {
			for _, pair := range g.fields {
				name, want := pair[0], pair[1]
				if stringOf(g.obj[name]) != want && field == "" {
					field = g.prefix + name
				}
			}
		}
		n, ok := m["state"].(json.Number)
		if (!ok || string(n) != "0") && field == "" {
			field = "message.state"
		}
		if m["isSentByCurrentUser"] != true && field == "" {
			field = "message.isSentByCurrentUser"
		}
		for _, name := range []string{"sendPipelineStatus", "sendPipelineOAMState", "streamingMetadata", "skypeeditedid", "originalNonLieMessage"} {
			if !undefined(m[name]) && field == "" {
				field = "message." + name
			}
		}
		if !undefined(p["sendAt"]) && field == "" {
			field = "properties.sendAt"
		}
		for _, name := range []string{"scheduledSendTime", "forwardMessageCardsCount", "forwardMessageOriginalConvId", "forwardTeamId"} {
			v, ok := c[name]
			if (!ok || v != nil) && field == "" {
				field = "clientDraftProperties." + name
			}
		}
		for _, name := range []string{"isForwardedMessage", "forwardPrivateReply"} {
			if c[name] != false && field == "" {
				field = "clientDraftProperties." + name
			}
		}
		for _, pair := range [][2]string{{"forwardAppIdsToInstall", "[]"}, {"forwardMessageIds", "[]"}, {"forwardMessageRecipients", "[]"}, {"forwardMessageCardContents", "null"}} {
			name, want := pair[0], pair[1]
			if stringOf(c[name]) != want && field == "" {
				field = "clientDraftProperties." + name
			}
		}
		if field != "" {
			out.Diagnostics = append(out.Diagnostics, guardDiagnostic(field, m, p, a, c))
			continue
		}
		body := Body{Envelope: e, WrapperConversationID: key[0], WrapperReplyChainID: key[1], MemberKey: k, MessageID: stringOf(m["id"]), ClientDraftID: presence(c, "clientDraftId"), LastEdit: editClock(c), VersioningTimestamp: presence(c, "versioningTimestamp"), Claim: claim(a)}
		body.Claim.ClientMessageID = presence(m, "clientMessageId")
		value := presence(m, "content")
		body.BodyState = value.State
		if value.Value != nil {
			body.HTML = value.Value
			text := teamsdesktop.HTMLToText(*value.Value)
			body.Text = &text
			body.BodyState = "readable"
			if *value.Value == "" {
				body.BodyState = "empty"
			}
		}
		if err := ctx.Err(); err != nil {
			return WrapperResult{}, err
		}
		tuple := []string{"teams-draft-id-v1", e.Source, e.TenantID, e.UserID, e.Database, e.Store, e.KeyJSON, k}
		tupleJSON, _ := json.Marshal(tuple)
		body.ID = "teams-draft:" + digest(tuple)
		body.BodyRevision = digest([]any{"teams-draft-body-v1", string(tupleJSON), string(raw), e.RemovedAt != nil, e.RemovedAt, DecoderQualification, ProjectorQualification})
		out.Bodies = append(out.Bodies, body)
	}
	if err := ctx.Err(); err != nil {
		return WrapperResult{}, err
	}
	return out, nil
}

func DecodeMetadata(raw []byte) (Metadata, error) {
	m, err := validateObject(raw)
	if err != nil {
		return Metadata{}, err
	}
	result := Metadata{Claim: claim(m), ConversationType: presence(m, "conversationType")}
	for _, p := range []Presence{result.Claim.SourceThreadID, result.Claim.SourceMessageID, result.Claim.ClientMessageID} {
		if p.State != "value" || p.Value == nil || *p.Value == "" {
			return Metadata{}, errors.New("incomplete metadata identity")
		}
	}
	return result, nil
}
func same(a, b Presence) bool {
	return a.State == "value" && b.State == "value" && a.Value != nil && b.Value != nil && *a.Value != "" && *a.Value == *b.Value
}
func MetadataMatches(body Body, m Metadata) (bool, bool) {
	a, b := body.Claim, m.Claim
	match := same(a.SourceThreadID, b.SourceThreadID) && same(a.SourceMessageID, b.SourceMessageID) && same(a.ClientMessageID, b.ClientMessageID)
	conflict := match && a.SourceReplyChainID.State == "value" && b.SourceReplyChainID.State == "value" && a.SourceReplyChainID.Value != nil && b.SourceReplyChainID.Value != nil && *a.SourceReplyChainID.Value != "" && *b.SourceReplyChainID.Value != "" && *a.SourceReplyChainID.Value != *b.SourceReplyChainID.Value
	return match, conflict
}
