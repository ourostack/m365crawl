package workiq

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeStubResult answers one tools/call for the synthetic write modes. die says the child exits
// after reading the request, without an answer.
func writeStubResult(mode string, params json.RawMessage) (result any, die bool) {
	var call struct {
		Name string         `json:"name"`
		Args map[string]any `json:"arguments"`
	}
	if json.Unmarshal(params, &call) != nil {
		return nil, true
	}
	if call.Name == "fetch" {
		return map[string]any{"structuredContent": map[string]any{"results": []any{map[string]any{"statusCode": 200,
			"data": map[string]any{"id": "synthetic-user", "mail": "test.user@example.invalid"}}}}}, false
	}
	want := map[string]any{"parentUrl": "/chats/48:notes/messages", "jsonBody": map[string]any{"body": map[string]any{"contentType": "text", "content": "hello"}}}
	if mode == "write-draft" {
		want = map[string]any{"parentUrl": "/me/messages", "jsonBody": map[string]any{"subject": "Plan", "body": map[string]any{"contentType": "text", "content": "hello"},
			"toRecipients": []any{map[string]any{"emailAddress": map[string]any{"address": "a@example.invalid"}}}}}
	}
	if call.Name != "create_entity" || !reflect.DeepEqual(call.Args, want) {
		return nil, true
	}
	switch mode {
	case "write-die":
		return nil, true
	case "write-hang":
		time.Sleep(time.Minute)
	case "write-draft":
		return map[string]any{"structuredContent": map[string]any{"statusCode": 201, "data": map[string]any{"id": "draft-1", "isDraft": true}}}, false
	}
	return map[string]any{"structuredContent": map[string]any{"statusCode": 201, "data": map[string]any{
		"id": "1700000000000", "chatId": "48:notes", "from": map[string]any{"user": map[string]any{"id": "synthetic-user"}}}}}, false
}

func openWriter(t *testing.T, mode string) *Writer {
	t.Helper()
	useStub(t, mode)
	w, err := OpenWriter(context.Background(), Invocation{}, "test.user@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestWorkIQWriterSendsOneFixedChatCreate(t *testing.T) {
	w := openWriter(t, "write-ok")
	page, err := w.Fetch(context.Background(), "/me")
	if err != nil || !strings.Contains(string(page.Data), "synthetic-user") {
		t.Fatal("writer reads", err)
	}
	got := w.CreateChatMessage(context.Background(), NotesToSelf, "hello")
	want := WriteAnswer{Kind: WriteCreated, Status: 201, ServerID: "1700000000000", ChatID: "48:notes", FromUserID: "synthetic-user"}
	if got != want {
		t.Fatalf("answer %+v", got)
	}
}

func TestWorkIQWriterSavesOneFixedDraft(t *testing.T) {
	w := openWriter(t, "write-draft")
	got := w.CreateMailDraft(context.Background(), "Plan", "hello", []string{"a@example.invalid"})
	if got.Kind != WriteCreated || got.ServerID != "draft-1" || !got.IsDraft {
		t.Fatalf("answer %+v", got)
	}
}

func TestWorkIQWriterNeverStartsAnInvalidOrCancelledCall(t *testing.T) {
	w := openWriter(t, "write-ok")
	for _, chat := range []string{"", "48:saved", "19:a/b", "19:a?x", "/me/messages", "19:" + strings.Repeat("a", 401)} {
		if got := w.CreateChatMessage(context.Background(), chat, "hello"); got.Kind != WriteRejected || got.Code != "workiq_path_invalid" {
			t.Fatalf("%q: %+v", chat, got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := w.CreateChatMessage(ctx, NotesToSelf, "hello"); got.Kind != WriteRejected || got.Code != "workiq_timeout" {
		t.Fatalf("cancelled %+v", got)
	}
	// Neither refusal reached the child: the session still answers the real call.
	if got := w.CreateChatMessage(context.Background(), NotesToSelf, "hello"); got.Kind != WriteCreated {
		t.Fatalf("after refusals %+v", got)
	}
}

func TestWorkIQWriterFailuresAfterTheRequestLeftAreUnknown(t *testing.T) {
	w := openWriter(t, "write-die")
	if got := w.CreateChatMessage(context.Background(), NotesToSelf, "hello"); got.Kind != WriteUnknown || got.Code == "" {
		t.Fatalf("lost session %+v", got)
	}
	old := requestTimeout
	requestTimeout = 300 * time.Millisecond
	t.Cleanup(func() { requestTimeout = old })
	w = openWriter(t, "write-hang")
	if got := w.CreateChatMessage(context.Background(), NotesToSelf, "hello"); got.Kind != WriteUnknown || got.Code != "workiq_timeout" {
		t.Fatalf("timeout %+v", got)
	}
	w = openWriter(t, "rpc-error-write")
	if got := w.CreateChatMessage(context.Background(), NotesToSelf, "hello"); got.Kind != WriteUnknown || got.Code != "workiq_signin_required" {
		t.Fatalf("rpc error %+v", got)
	}
}

func TestWorkIQWriteAnswerClassification(t *testing.T) {
	created := `{"structuredContent":{"statusCode":201,"data":{"id":"m1","chatId":"c","from":{"user":{"id":"u"}}}}}`
	for _, tc := range []struct {
		raw  string
		kind WriteKind
		code string
	}{
		{created, WriteCreated, ""},
		{`{"structuredContent":{"results":[{"statusCode":200,"data":{"id":"m1"}}]}}`, WriteCreated, ""},
		{`{"structuredContent":{"statusCode":201,"data":{}}}`, WriteUnknown, "workiq_protocol"},
		{`{"structuredContent":{"statusCode":201,"data":{"id":"` + strings.Repeat("x", maxPath+1) + `"}}}`, WriteUnknown, "workiq_protocol"},
		{`{"structuredContent":{"statusCode":201,"data":{"id":"m"},"error":{"code":"X"}}}`, WriteUnknown, "workiq_failed"},
		{`{"isError":true,"structuredContent":{"statusCode":400,"error":{"error":{"code":"BadRequest","message":"Private text"}}}}`, WriteRejected, "BadRequest"},
		{`{"isError":true,"structuredContent":{"statusCode":403,"error":{"code":"Forbidden"}}}`, WriteRejected, "Forbidden"},
		{`{"isError":true,"structuredContent":{"statusCode":400,"error":"Access denied for POST path: /chats/x"}}`, WriteRejected, "workiq_policy_denied"},
		{`{"isError":true,"structuredContent":{"statusCode":400,"error":"something else"}}`, WriteUnknown, "workiq_failed"},
		{`{"isError":true,"structuredContent":{"statusCode":400,"error":{"code":"has space"}}}`, WriteUnknown, "workiq_failed"},
		{`{"isError":true,"structuredContent":{"statusCode":400,"error":{}}}`, WriteUnknown, "workiq_failed"},
		{`{"isError":true,"structuredContent":{"statusCode":408,"error":{"code":"Timeout"}}}`, WriteUnknown, "workiq_failed"},
		{`{"isError":true,"structuredContent":{"statusCode":400}}`, WriteUnknown, "workiq_failed"},
		{`{"isError":true,"structuredContent":{"statusCode":503,"error":{"code":"ServiceUnavailable"}}}`, WriteUnknown, "workiq_failed"},
		{`{"isError":true,"content":[{"type":"text","text":"tool failure"}]}`, WriteUnknown, "workiq_failed"},
		{`{"structuredContent":"text"}`, WriteUnknown, "workiq_protocol"},
		{`{"a":1,"a":2}`, WriteUnknown, "workiq_protocol"},
		{strings.Repeat(" ", maxFrame+1), WriteUnknown, "workiq_protocol"},
	} {
		got := decodeWrite(context.Background(), []byte(tc.raw))
		if got.Kind != tc.kind || got.Code != tc.code && tc.code != "" || tc.kind == WriteCreated && got.Code != "" {
			t.Fatalf("%.80s: %+v", tc.raw, got)
		}
		if strings.Contains(got.Server, "Private") || strings.Contains(got.Server, "/chats/x") {
			t.Fatalf("descriptor leaked server text: %q", got.Server)
		}
	}
	if got := decodeWrite(context.Background(), []byte(`{"structuredContent":{"statusCode":400,"error":{"error":{"code":"BadRequest"}}}}`)); got.Server != "status 400; code BadRequest" {
		t.Fatalf("descriptor %q", got.Server)
	}
}

func TestWorkIQDecodeWrittenMessage(t *testing.T) {
	m, err := DecodeWrittenMessage(context.Background(), json.RawMessage(`{"id":"m1","chatId":"48:notes","createdDateTime":"2026-10-31T11:51:27.955Z",
		"from":{"user":{"id":"u"}},"body":{"contentType":"html","content":"<p>Hello <b>there</b></p>"}}`))
	if err != nil || m.ID != "m1" || m.ChatID != "48:notes" || m.FromUserID != "u" || m.Deleted || m.Text != "Hello there" ||
		m.Created != time.Date(2026, 10, 31, 11, 51, 27, 955e6, time.UTC) {
		t.Fatalf("%+v %v", m, err)
	}
	m, err = DecodeWrittenMessage(context.Background(), json.RawMessage(`{"id":"d1","isDraft":true,"subject":"S","body":{"contentType":"text","content":"x"},
		"toRecipients":[{"emailAddress":{"address":" A@Example.invalid "}}],"deletedDateTime":"2026-10-31T00:00:00Z"}`))
	if err != nil || !m.IsDraft || m.Subject != "S" || m.Text != "x" || !m.Deleted || fmt.Sprint(m.Recipients) != "[a@example.invalid]" || !m.Created.IsZero() {
		t.Fatalf("%+v %v", m, err)
	}
	for _, raw := range []string{`{"chatId":"c"}`, `{"id":"m","createdDateTime":"yesterday"}`, `{"id":1}`} {
		if _, err := DecodeWrittenMessage(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}

func TestWorkIQScrubTextMatchesTheReadBack(t *testing.T) {
	if got := ScrubText("plain words"); got != "plain words" {
		t.Fatal(got)
	}
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJlc2lnbmF0dXJl"
	if got := ScrubText("token " + jwt); strings.Contains(got, jwt) {
		t.Fatal("not scrubbed", got)
	}
	old := scrubServer
	scrubServer = func(b []byte) ([]byte, int) { return []byte("not json"), 0 }
	t.Cleanup(func() { scrubServer = old })
	if got := ScrubText("kept"); got != "kept" {
		t.Fatal(got)
	}
}

func TestWorkIQOpenWriterKeepsTheSpawnGuard(t *testing.T) {
	t.Setenv("M365CRAWL_WORKIQ_BIN", "")
	_, err := OpenWriter(context.Background(), Invocation{}, "")
	requireCode(t, err, "workiq_hermetic_required")
}
