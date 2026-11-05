package workiq

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/teamsimage"
)

const imageTestPath = "/chats/19:synthetic-chat/messages/1001/hostedContents/opaque%3Dvalue/$value"

func imageTestMessage(path, clock string) json.RawMessage {
	body := `<img itemid="synthetic-image" itemtype="http://schema.skype.com/AMSImage" src="` + path + `" width="17" height="9">`
	wire := map[string]any{"id": "1001", "messageType": "message", "body": map[string]any{"contentType": "html", "content": body}}
	if clock != "" {
		wire["lastModifiedDateTime"] = clock
	}
	raw, _ := json.Marshal(wire)
	return raw
}

func TestSelectedImageMessageIdentityAndFamily(t *testing.T) {
	ctx := context.Background()
	raw := imageTestMessage(imageTestPath, "")
	message, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", raw)
	if err != nil || message.ModifiedAt != nil {
		t.Fatalf("valid exact message with unknown modification clock: %v", err)
	}
	ref, err := BindChatImage(message, "synthetic-image")
	if err != nil || ref.resource != imageTestPath {
		t.Fatalf("exact hosted path not retained: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"wrong-message": func(w map[string]any) { w["id"] = "1002" },
		"system-event":  func(w map[string]any) { w["messageType"] = "systemEventMessage" },
		"reply":         func(w map[string]any) { w["replyToId"] = "1000" },
		"removed":       func(w map[string]any) { w["@removed"] = map[string]any{} },
		"empty-removed": func(w map[string]any) { w["@removed"] = "" },
		"deleted":       func(w map[string]any) { w["deletedDateTime"] = "2026-01-01T00:00:00Z" },
		"missing-body":  func(w map[string]any) { delete(w, "body") },
		"text-body":     func(w map[string]any) { w["body"] = map[string]any{"contentType": "text", "content": "hello"} },
		"channel":       func(w map[string]any) { w["channelIdentity"] = map[string]any{"channelId": "synthetic-channel"} },
		"invalid-clock": func(w map[string]any) { w["lastModifiedDateTime"] = "not-a-clock" },
		"empty-clock":   func(w map[string]any) { w["lastModifiedDateTime"] = "" },
	} {
		t.Run(name, func(t *testing.T) {
			var w map[string]any
			if err := json.Unmarshal(raw, &w); err != nil {
				t.Fatal(err)
			}
			mutate(w)
			changed, err := json.Marshal(w)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", changed); err == nil {
				t.Fatal("unsupported identity/family/body admitted")
			}
		})
	}
	for _, chat := range []string{"", "../chat", "chat|other", "chat\nother"} {
		if _, err := DecodeSelectedImageMessage(ctx, chat, "1001", raw); err == nil {
			t.Fatal("invalid requested chat admitted")
		}
	}
	user := User{ID: "synthetic-user", Mail: "synthetic@example.invalid"}
	chat := Chat{ID: "19:synthetic-chat", TenantID: "synthetic-tenant", Type: "group"}
	fact := AccountFact{ID: "synthetic-tenant/synthetic-user", Family: "chats", UserID: user.ID, TenantID: chat.TenantID}
	if _, err := SelectChatAccount(user, chat, []AccountFact{fact}, fact.ID); err != nil {
		t.Fatal(err)
	}
	for _, mismatch := range []AccountFact{
		{ID: fact.ID, Family: "mail", UserID: fact.UserID, TenantID: fact.TenantID},
		{ID: fact.ID, Family: "chats", UserID: "other-user", TenantID: fact.TenantID},
		{ID: fact.ID, Family: "chats", UserID: fact.UserID, TenantID: "other-tenant"},
	} {
		if _, err := SelectChatAccount(user, chat, []AccountFact{mismatch}, fact.ID); err == nil {
			t.Fatal("mismatched archived identity admitted")
		}
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, message), "synthetic") ||
				strings.Contains(fmt.Sprintf(format, ref), "synthetic") {
				t.Fatal("private native observation or resource leaked through formatting")
			}
		}
	}
}

func TestChatImagePathRejectsEncodedScopeEscape(t *testing.T) {
	ctx := context.Background()
	for _, path := range []string{imageTestPath, "https://graph.microsoft.com/v1.0" + imageTestPath} {
		message, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", imageTestMessage(path, ""))
		if err != nil {
			t.Fatal(err)
		}
		ref, err := BindChatImage(message, "synthetic-image")
		if err != nil || ref.resource != imageTestPath {
			t.Fatalf("fixed Graph route/encoding lost: %v", err)
		}
	}
	for _, path := range []string{
		strings.Replace(imageTestPath, "synthetic-chat", "other-chat", 1),
		strings.Replace(imageTestPath, "/1001/", "/1002/", 1),
		strings.Replace(imageTestPath, "opaque%3Dvalue", "%2Fescape", 1),
		strings.Replace(imageTestPath, "opaque%3Dvalue", "%5Cescape", 1),
		strings.Replace(imageTestPath, "opaque%3Dvalue", "%252Fescape", 1),
		strings.Replace(imageTestPath, "opaque%3Dvalue", "%2e%2e", 1),
		strings.Replace(imageTestPath, "opaque%3Dvalue", "%252e%252e", 1),
		strings.Replace(imageTestPath, "opaque%3Dvalue", "..", 1),
		strings.Replace(imageTestPath, "opaque%3Dvalue", "", 1),
		imageTestPath + "?token=synthetic",
		imageTestPath + "#fragment",
		"https://graph.microsoft.com:443/v1.0" + imageTestPath,
		"https://synthetic@graph.microsoft.com/v1.0" + imageTestPath,
		"https://cdn.example.invalid" + imageTestPath,
		"https://graph.microsoft.com/beta" + imageTestPath,
		"//graph.microsoft.com/v1.0" + imageTestPath,
		strings.Replace(imageTestPath, "/$value", "", 1),
	} {
		message, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", imageTestMessage(path, ""))
		if err != nil {
			t.Fatal("fixture body failed before path admission", err)
		}
		if _, err := BindChatImage(message, "synthetic-image"); err == nil {
			t.Fatal("unscoped native resource admitted")
		}
	}
}

func TestChatImageClockRecheck(t *testing.T) {
	ctx := context.Background()
	before, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", imageTestMessage(imageTestPath, "2026-01-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	for _, clock := range []string{"2026-01-01T00:00:00Z", "2026-01-01T01:00:00+01:00"} {
		after, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", imageTestMessage(imageTestPath, clock))
		if err != nil || SameImageObservation(before, after, "synthetic-image") != nil {
			t.Fatal("equal actual modification instants refused", err)
		}
	}
	unknown, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", imageTestMessage(imageTestPath, ""))
	if err != nil || SameImageObservation(unknown, unknown, "synthetic-image") != nil {
		t.Fatal("two absent clocks are not unknown/matching", err)
	}
	for _, raw := range []json.RawMessage{
		imageTestMessage(imageTestPath, ""),
		imageTestMessage(imageTestPath, "2026-01-01T00:00:01Z"),
		imageTestMessage(strings.Replace(imageTestPath, "opaque%3Dvalue", "other", 1), "2026-01-01T00:00:00Z"),
	} {
		after, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := SameImageObservation(before, after, "synthetic-image"); err == nil {
			t.Fatal("changed or unverifiable selected observation admitted")
		}
	}
	duplicate := imageTestMessage(imageTestPath, "")
	var w map[string]any
	if err := json.Unmarshal(duplicate, &w); err != nil {
		t.Fatal(err)
	}
	body := w["body"].(map[string]any)
	body["content"] = body["content"].(string) + `<img itemid="synthetic-image" src="/chats/19:synthetic-chat/messages/1001/hostedContents/other/$value">`
	duplicate, err = json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	ambiguous, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BindChatImage(ambiguous, "synthetic-image"); err == nil {
		t.Fatal("ambiguous selected binding admitted")
	}
	body["content"] = `<img itemid="synthetic-image" src="` + imageTestPath + `">` +
		strings.Repeat("x", 65537)
	partialRaw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", partialRaw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BindChatImage(partial, "synthetic-image"); err == nil {
		t.Fatal("selected native prefix authorized despite incomplete inspection")
	}
}

func TestChatImageBlobEnvelope(t *testing.T) {
	ctx := context.Background()
	good := `{"structuredContent":{"statusCode":200,"contentType":"image/png","sizeBytes":4,"base64Content":"AQIDBA=="}}`
	blob, err := decodeImageBlob(ctx, []byte(good))
	if err != nil || blob.HTTPStatus != 200 || blob.ContentType != "image/png" || string(blob.Data) != "\x01\x02\x03\x04" {
		t.Fatalf("binary envelope changed: %v", err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"image/png"`, `"image/jpeg"`, 1),
		strings.Replace(good, `"sizeBytes":4`, `"sizeBytes":4194305`, 1),
		strings.Replace(good, `"sizeBytes":4`, `"sizeBytes":3`, 1),
		strings.Replace(good, `"statusCode":200`, `"statusCode":206`, 1),
		strings.Replace(good, `"contentType":"image/png",`, "", 1),
		strings.Replace(good, `"AQIDBA=="`, `"not base64"`, 1),
		`{"structuredContent":{"statusCode":200,"contentType":"image/png","sizeBytes":0,"base64Content":""}}`,
		`{"structuredContent":{"statusCode":200,"statusCode":404,"contentType":"image/png","sizeBytes":4,"base64Content":"AQIDBA=="}}`,
	} {
		if _, err := decodeImageBlob(ctx, []byte(bad)); err == nil {
			t.Fatal("invalid binary envelope admitted")
		}
	}
	blob, err = decodeImageBlob(ctx, []byte(`{"structuredContent":{"statusCode":404}}`))
	if err == nil || blob.HTTPStatus != 404 {
		t.Fatal("service refusal status lost", err)
	}
	exact := fmt.Sprintf(`{"structuredContent":{"statusCode":200,"contentType":"image/png","sizeBytes":4194304,"base64Content":%q}}`,
		base64.StdEncoding.EncodeToString(make([]byte, 4194304)))
	blob, err = decodeImageBlob(ctx, []byte(exact))
	if err != nil || len(blob.Data) != 4194304 {
		t.Fatal("exact binary byte limit refused", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := decodeImageBlob(ctx, []byte(good)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was not preserved", err)
	}
	if _, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", imageTestMessage(imageTestPath, "")); !errors.Is(err, context.Canceled) {
		t.Fatal("message cancellation was not preserved", err)
	}
}

func TestChatImageLegacyDocumentUnchanged(t *testing.T) {
	ctx := context.Background()
	for _, contentType := range []string{`""`, `"application/x"`, `"image/jpeg"`, `17`, `{}`} {
		raw := fmt.Sprintf(`{"structuredContent":{"statusCode":200,"contentType":%s,"sizeBytes":0,"base64Content":""}}`, contentType)
		data, status, err := decodeBlob(ctx, []byte(raw))
		if err != nil || status != 200 || len(data) != 0 {
			t.Fatal("image constraints leaked into legacy document decoder", err)
		}
	}
	if IDQualified(ClassChat) || IDQualified(ClassChannelRoot) || IDQualified(ClassChannelReply) {
		t.Fatal("ordinary source ID qualification changed")
	}
	account := AccountFact{Family: "chats", TenantID: "synthetic-tenant", UserID: "synthetic-user"}
	chat := Chat{ID: "19:synthetic-chat", TenantID: account.TenantID, Type: "group"}
	if _, err := DecodeChatMessage(ctx, account, chat, imageTestMessage(imageTestPath, "")); err == nil {
		t.Fatal("image optional-clock policy weakened full-sync message contract")
	}
}

func imageStubResult(raw json.RawMessage) any {
	var params struct {
		Name string `json:"name"`
		Args struct {
			Path string `json:"path"`
		} `json:"arguments"`
	}
	if json.Unmarshal(raw, &params) != nil || params.Name != "fetch_blob" || params.Args.Path != imageTestPath {
		return map[string]any{"isError": true}
	}
	return map[string]any{"structuredContent": map[string]any{"statusCode": 200, "contentType": "image/png",
		"sizeBytes": 4, "base64Content": base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4})}}
}

func TestSelectedImageMalformedEntityAndUnboundObservation(t *testing.T) {
	ctx := context.Background()
	if _, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", json.RawMessage(`{`)); err == nil {
		t.Fatal("malformed native entity produced an image observation")
	}
	if _, err := BindChatImage(ImageMessage{}, "synthetic-image"); err == nil {
		t.Fatal("unbound observation produced an authorized image reference")
	}
	message, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", imageTestMessage(imageTestPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]ImageMessage{{{}, message}, {message, {}}} {
		if err := SameImageObservation(pair[0], pair[1], "synthetic-image"); err == nil {
			t.Fatal("invalid first or second native observation certified a stable reference")
		}
	}
	for _, path := range []string{"chats/19:synthetic-chat/messages/1001/hostedContents/value/$value", ""} {
		if resource, err := chatImageResource(path, "19:synthetic-chat", "1001"); err == nil || resource != "" {
			t.Fatal("relative or absent native path authorized transport", resource, err)
		}
	}
	ref, err := BindChatImage(message, "synthetic-image")
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{imageTestPath + "?ticket=synthetic", "https://graph.microsoft.com/v1.0" + imageTestPath} {
		forged := ref
		forged.resource = resource
		if blob, err := new(Client).FetchChatImageBlob(ctx, forged); err == nil || len(blob.Data) != 0 {
			t.Fatal("forged/noncanonical reference reached an absent native transport", err)
		}
	}
}

func TestChatImageInspectionFailureCannotAuthorizeReference(t *testing.T) {
	message, err := DecodeSelectedImageMessage(context.Background(), "19:synthetic-chat", "1001", imageTestMessage(imageTestPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	oldInspect := chatImageInspect
	t.Cleanup(func() { chatImageInspect = oldInspect })
	sentinel := errors.New("synthetic image inspection failure")
	chatImageInspect = func(context.Context, string) (teamsimage.Inspection, error) {
		return teamsimage.Inspection{}, sentinel
	}
	ref, err := BindChatImage(message, "synthetic-image")
	if !errors.Is(err, sentinel) || ref.resource != "" || ref.binding != "" {
		t.Fatal("inspection failure produced an authorized native resource", err)
	}
}
func TestChatImageTransportAndProtocolFailureCloseSession(t *testing.T) {
	ctx := context.Background()
	message, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", imageTestMessage(imageTestPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := BindChatImage(message, "synthetic-image")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"rpc-error-write", "doc"} {
		t.Run(mode, func(t *testing.T) {
			useStub(t, mode)
			client, err := Open(ctx, Invocation{ExplicitTeamsRoot: true}, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := client.Close(); err != nil {
					t.Error(err)
				}
			})
			blob, err := client.FetchChatImageBlob(ctx, ref)
			if err == nil || blob.HTTPStatus != 0 || len(blob.Data) != 0 ||
				strings.Contains(err.Error(), "private-download") {
				t.Fatal("native transport/protocol failure returned bytes or private diagnostics", blob.HTTPStatus, err)
			}
			select {
			case <-client.drained:
			default:
				t.Fatal("failed image transport left its native session running")
			}
		})
	}
}
func TestChatImageFixedBlobCall(t *testing.T) {
	ctx := context.Background()
	message, err := DecodeSelectedImageMessage(ctx, "19:synthetic-chat", "1001", imageTestMessage(imageTestPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := BindChatImage(message, "synthetic-image")
	if err != nil {
		t.Fatal(err)
	}
	useStub(t, "image")
	client, err := Open(ctx, Invocation{ExplicitTeamsRoot: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()
	blob, err := client.FetchChatImageBlob(ctx, ref)
	if err != nil || string(blob.Data) != "\x01\x02\x03\x04" || blob.ContentType != "image/png" {
		t.Fatal("fixed exact-scope binary call failed", err)
	}
	// A zero client has no transport: a forged reference must refuse before it is used.
	if _, err := new(Client).FetchChatImageBlob(ctx, ChatImageRef{}); err == nil {
		t.Fatal("unbound reference attempted transport")
	}
}
