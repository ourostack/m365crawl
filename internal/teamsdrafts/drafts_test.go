package teamsdrafts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Envelope, map[string]any, map[string]any) {
	t.Helper()
	u := map[string]any{"$undefined": true}
	c := map[string]any{"clientDraftId": "synthetic-client", "lastEditTimestamp": 12, "versioningTimestamp": "12", "scheduledSendTime": nil,
		"isForwardedMessage": false, "forwardPrivateReply": false, "forwardAppIdsToInstall": "[]", "forwardMessageIds": "[]", "forwardMessageRecipients": "[]",
		"forwardMessageCardContents": "null", "forwardMessageCardsCount": nil, "forwardMessageOriginalConvId": nil, "forwardTeamId": nil}
	m := map[string]any{"id": "message-b", "type": "Message", "draftType": "RegularDraft", "threadType": "streamofdrafts", "messageType": "RichText/Html", "contentType": "Text",
		"state": 0, "isSentByCurrentUser": true, "creator": "8:orgid:user", "content": "<p>Hello 🌱</p>", "clientMessageId": "client-message",
		"lieType": "Send", "sendPipelineStatus": u, "sendPipelineOAMState": u, "streamingMetadata": u, "skypeeditedid": u, "originalNonLieMessage": u,
		"properties":   map[string]any{"sendAt": u, "activity": map[string]any{"sourceUserId": "8:orgid:user", "activityType": "Draft", "activitySubtype": "Draft", "sourceThreadId": "target", "sourceMessageId": "target-message"}},
		"draftDetails": map[string]any{"clientDraftProperties": c}}
	w := map[string]any{"conversationId": "wrapper", "replyChainId": "reply", "messageMap": map[string]any{"member-a": m}}
	e := Envelope{Source: "P|https_teams.microsoft.com_0", TenantID: "tenant", UserID: "user", Database: "Teams:streams-replychain-manager:react-web-client:tenant:user:en", Store: "drafts", KeyJSON: `["wrapper","reply"]`}
	return e, w, m
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decode(t *testing.T, e Envelope, w any) WrapperResult {
	t.Helper()
	r, err := DecodeWrapper(context.Background(), e, encode(t, w), 4096)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDecodeWrapperSeparateIdentities(t *testing.T) {
	e, w, _ := fixture(t)
	r := decode(t, e, w)
	if !r.Complete || len(r.Bodies) != 1 {
		t.Fatalf("complete own body: %+v", r)
	}
	b := r.Bodies[0]
	if b.MemberKey != "member-a" || b.MessageID != "message-b" || b.ID == "" {
		t.Fatal(b)
	}
	for _, key := range []string{`["wrong","reply"]`, `[]`, `["wrapper","reply","extra"]`} {
		x := e
		x.KeyJSON = key
		if r := decode(t, x, w); r.Complete || len(r.Bodies) != 0 {
			t.Fatal("invalid namespace admitted", key, r)
		}
	}
	raw := []byte(`{"conversationId":"wrapper","replyChainId":"reply","messageMap":{"x":{},"x":{}}}`)
	r, err := DecodeWrapper(context.Background(), e, raw, 4096)
	if err != nil || r.Complete || len(r.Bodies) != 0 {
		t.Fatal(r, err)
	}
	invalid := e
	invalid.KeyJSON = "[\"" + string([]byte{0xff}) + "\",\"reply\"]"
	w["conversationId"] = "�"
	if r := decode(t, invalid, w); r.Complete {
		t.Fatal("invalid UTF-8 key admitted")
	}
}

func TestDecodeWrapperDiagnosticCategories(t *testing.T) {
	for _, tc := range []struct {
		value    any
		category string
	}{
		{"0", "string"}, {false, "boolean"}, {json.Number("1"), "number"}, {nil, "null"}, {[]any{}, "array"}, {map[string]any{"other": true}, "object"}, {map[string]any{"$undefined": true}, "undefined"},
	} {
		e, w, m := fixture(t)
		m["state"] = tc.value
		r := decode(t, e, w)
		if len(r.Diagnostics) != 1 || r.Diagnostics[0].Field != "message.state" || r.Diagnostics[0].Category != tc.category {
			t.Fatal(tc.category, r.Diagnostics)
		}
	}
	e, w, m := fixture(t)
	delete(m, "type")
	r := decode(t, e, w)
	if len(r.Diagnostics) != 1 || r.Diagnostics[0].Category != "missing" || r.Diagnostics[0].Code != "missing_discriminator" {
		t.Fatal(r.Diagnostics)
	}
}

func TestDecodeWrapperSourceOwnership(t *testing.T) {
	for _, s := range []string{"P|https_teams.microsoft.com_0", "Profile A|https_teams.cloud.microsoft_443"} {
		if !EligibleSource(s) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"|https_teams.microsoft.com_0", ".|https_teams.microsoft.com_0", "..|https_teams.microsoft.com_0", "P/x|https_teams.microsoft.com_0", "P\\x|https_teams.microsoft.com_0", "P\x00|https_teams.microsoft.com_0", "P||https_teams.microsoft.com_0", "P|https_TEAMS.microsoft.com_0", "P|https_evil.com_0"} {
		if EligibleSource(s) {
			t.Fatal(s)
		}
	}
	for _, field := range []string{"creator", "type", "isSentByCurrentUser"} {
		e, w, m := fixture(t)
		m[field] = "wrong"
		if len(decode(t, e, w).Bodies) != 0 {
			t.Fatal(field)
		}
	}
	e, w, _ := fixture(t)
	e.UserID = "other"
	if len(decode(t, e, w).Bodies) != 0 {
		t.Fatal("account mismatch")
	}
}

func TestDecodeWrapperGuardRepresentations(t *testing.T) {
	type field struct{ group, name string }
	fields := []field{{"m", "type"}, {"m", "draftType"}, {"m", "threadType"}, {"m", "messageType"}, {"m", "contentType"}, {"m", "state"}, {"m", "isSentByCurrentUser"}, {"a", "activityType"}, {"a", "activitySubtype"}, {"c", "scheduledSendTime"}, {"p", "sendAt"}, {"m", "sendPipelineStatus"}, {"m", "sendPipelineOAMState"}, {"m", "streamingMetadata"}, {"m", "skypeeditedid"}, {"m", "originalNonLieMessage"}, {"m", "lieType"}, {"c", "isForwardedMessage"}, {"c", "forwardPrivateReply"}, {"c", "forwardAppIdsToInstall"}, {"c", "forwardMessageIds"}, {"c", "forwardMessageRecipients"}, {"c", "forwardMessageCardContents"}, {"c", "forwardMessageCardsCount"}, {"c", "forwardMessageOriginalConvId"}, {"c", "forwardTeamId"}}
	for _, f := range fields {
		t.Run(f.name, func(t *testing.T) {
			e, w, m := fixture(t)
			p := m["properties"].(map[string]any)
			a := p["activity"].(map[string]any)
			c := m["draftDetails"].(map[string]any)["clientDraftProperties"].(map[string]any)
			obj := map[string]map[string]any{"m": m, "p": p, "a": a, "c": c}[f.group]
			valid := obj[f.name]
			if len(decode(t, e, w).Bodies) != 1 {
				t.Fatal("supported fixture withheld")
			}
			for _, bad := range []any{nil, map[string]any{"$undefined": true}, true, false, 0, "wrong", []any{}, " [ ] ", "\"[]\"", "null"} {
				if string(encode(t, bad)) == string(encode(t, valid)) {
					continue
				}
				obj[f.name] = bad
				r := decode(t, e, w)
				if len(r.Bodies) != 0 || len(r.Diagnostics) == 0 {
					t.Fatalf("admitted %s=%#v: %+v", f.name, bad, r)
				}
			}
			delete(obj, f.name)
			if len(decode(t, e, w).Bodies) != 0 {
				t.Fatal("missing guard")
			}
		})
	}
}

func TestDecodeWrapperBodyAndClocks(t *testing.T) {
	for _, tc := range []struct {
		value any
		state string
	}{{nil, "null"}, {map[string]any{"$undefined": true}, "undefined"}, {19, "invalid"}, {"", "empty"}, {"<p>Hello 🌱</p>", "readable"}} {
		e, w, m := fixture(t)
		m["content"] = tc.value
		b := decode(t, e, w).Bodies[0]
		if b.BodyState != tc.state {
			t.Fatal(tc, b)
		}
		if tc.state == "readable" && (b.Text == nil || *b.Text != "Hello 🌱") {
			t.Fatal(b)
		}
	}
	e, w, m := fixture(t)
	delete(m, "content")
	if b := decode(t, e, w).Bodies[0]; b.BodyState != "missing" || b.Text != nil {
		t.Fatal(b)
	}
	c := m["draftDetails"].(map[string]any)["clientDraftProperties"].(map[string]any)
	for _, bad := range []any{"12", 1.5, json.Number("9223372036854775808")} {
		c["lastEditTimestamp"] = bad
		if b := decode(t, e, w).Bodies[0]; b.LastEdit.State != "invalid" {
			t.Fatal(b.LastEdit)
		}
	}
}

func TestDecodeWrapperBodyFrameRevision(t *testing.T) {
	e, w, _ := fixture(t)
	raw := encode(t, w)
	b := decode(t, e, w).Bodies[0]
	tuple := encode(t, []string{"teams-draft-id-v1", e.Source, e.TenantID, e.UserID, e.Database, e.Store, e.KeyJSON, "member-a"})
	h := sha256.Sum256(tuple)
	if b.ID != "teams-draft:"+hex.EncodeToString(h[:]) {
		t.Fatal(b.ID)
	}
	frame := encode(t, []any{"teams-draft-body-v1", string(tuple), string(raw), false, nil, "teams-draft-decoder-v1", "teams-html-to-text-v1"})
	h = sha256.Sum256(frame)
	if b.BodyRevision != hex.EncodeToString(h[:]) {
		t.Fatal(b.BodyRevision)
	}
	e.UpdatedAt = "new"
	if decode(t, e, w).Bodies[0].BodyRevision != b.BodyRevision {
		t.Fatal("update clock guarded")
	}
	now := "removed"
	e.RemovedAt = &now
	if decode(t, e, w).Bodies[0].BodyRevision == b.BodyRevision {
		t.Fatal("removal not guarded")
	}
	r, err := DecodeWrapper(context.Background(), e, append(raw, ' '), 4096)
	if err != nil || r.Bodies[0].BodyRevision == b.BodyRevision {
		t.Fatal(r, err)
	}
}

func TestDecodeWrapperMemberBudgetAndCancellation(t *testing.T) {
	e, w, m := fixture(t)
	members := map[string]any{}
	for i := 0; i < 4096; i++ {
		members[strconv.Itoa(i)] = map[string]any{"id": "unsupported"}
	}
	w["messageMap"] = members
	r := decode(t, e, w)
	if !r.Complete || r.VisitedMembers != 4096 {
		t.Fatal("exact member boundary", r.Complete, r.VisitedMembers)
	}
	members["overflow"] = map[string]any{"id": "unsupported"}
	r = decode(t, e, w)
	if r.Complete || len(r.Bodies) != 0 || r.VisitedMembers != 4096 {
		t.Fatal("member cap", r.Complete, r.VisitedMembers)
	}
	// Exercise the same member exhaustion using a small caller allowance.
	w["messageMap"] = map[string]any{"a": m, "b": m}
	r, err := DecodeWrapper(context.Background(), e, encode(t, w), 1)
	if err != nil || r.Complete || len(r.Bodies) != 0 || r.VisitedMembers != 1 {
		t.Fatal(r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := DecodeWrapper(ctx, e, encode(t, w), 4096); !errors.Is(err, context.Canceled) || len(r.Bodies) != 0 {
		t.Fatal(r, err)
	}
}

func TestMetadataMatchesCompound(t *testing.T) {
	e, w, _ := fixture(t)
	b := decode(t, e, w).Bodies[0]
	m, err := DecodeMetadata([]byte(`{"sourceThreadId":"target","sourceMessageId":"target-message","clientMessageId":"client-message"}`))
	if err != nil {
		t.Fatal(err)
	}
	if match, conflict := MetadataMatches(b, m); !match || conflict {
		t.Fatal(match, conflict)
	}
	m.Claim.ClientMessageID = Presence{State: "value", Value: new("other")}
	if match, _ := MetadataMatches(b, m); match {
		t.Fatal("target-only match")
	}
	if _, err := DecodeMetadata([]byte(`{"sourceThreadId":"target"}`)); err == nil {
		t.Fatal("incomplete possible match accepted as irrelevant")
	}
}

type cancellingContext struct {
	context.Context
	cancel    context.CancelFunc
	calls, at int
}

func (c *cancellingContext) Err() error {
	c.calls++
	if c.calls == c.at {
		c.cancel()
	}
	return c.Context.Err()
}
func TestDecodeWrapperTerminalCancellation(t *testing.T) {
	e, w, _ := fixture(t)
	for _, at := range []int{2, 3, 4} {
		ctx, cancel := context.WithCancel(context.Background())
		c := &cancellingContext{Context: ctx, cancel: cancel, at: at}
		r, err := DecodeWrapper(c, e, encode(t, w), 4096)
		cancel()
		if !errors.Is(err, context.Canceled) || len(r.Bodies) != 0 {
			t.Fatalf("checkpoint%d: %+v %v", at, r, err)
		}
	}
}

func TestDecodeWrapperMalformedBoundaries(t *testing.T) {
	e, _, _ := fixture(t)
	for _, raw := range [][]byte{{0xff}, []byte(`{} {}`), []byte(`null`), []byte(`[]`), []byte(`{`), []byte(`{"x":`), []byte(`{"x"`), []byte(`{"x":[}`), []byte(`{"conversationId":"wrapper","replyChainId":"reply"}`), []byte(`{"conversationId":"wrapper","replyChainId":"reply","messageMap":{"":{"id":"x"}}}`)} {
		r, err := DecodeWrapper(context.Background(), e, raw, 4096)
		if err != nil || r.Complete || len(r.Bodies) != 0 {
			t.Fatal(string(raw), r, err)
		}
	}
	if _, err := DecodeMetadata([]byte(`[]`)); err == nil {
		t.Fatal("metadata array")
	}
	e, w, m := fixture(t)
	m["properties"].(map[string]any)["activity"].(map[string]any)["sourceUserId"] = "other"
	if len(decode(t, e, w).Bodies) != 0 {
		t.Fatal("activity ownership ignored")
	}
	e, w, m = fixture(t)
	m["sendPipelineStatus"] = map[string]any{"$undefined": true, "extra": true}
	if len(decode(t, e, w).Bodies) != 0 {
		t.Fatal("extra undefined member admitted")
	}
	e, w, m = fixture(t)
	m["content"] = "<p>one</p>"
	delete(m["draftDetails"].(map[string]any)["clientDraftProperties"].(map[string]any), "lastEditTimestamp")
	if b := decode(t, e, w).Bodies[0]; b.LastEdit.State != "missing" {
		t.Fatal(b.LastEdit)
	}
	d := json.NewDecoder(strings.NewReader(`[1]`))
	if _, err := d.Token(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Token(); err != nil {
		t.Fatal(err)
	}
	if err := uniqueValue(d); err == nil {
		t.Fatal("unexpected closing delimiter accepted")
	}
}
