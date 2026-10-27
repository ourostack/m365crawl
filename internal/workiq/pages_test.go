package workiq

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkIQCursorScope(t *testing.T) {
	const scope = "/me/mailFolders/f/messages/delta()"
	const relative = "/me/mailFolders/f/messages/delta()?$deltatoken=a%2Fb%2Bz&x=2&x=1"
	got, err := RelativePath("https://graph.microsoft.com/v1.0"+relative, scope)
	if err != nil || got != relative {
		t.Fatalf("opaque bytes changed: %q %v", got, err)
	}
	for _, path := range []string{
		"", "//evil.invalid/me", "https://evil.invalid/me", "/organization", "/me#fragment",
		"/me/../other", "/me/%2e%2e/other", "/me/..", "/me/.", "/me/%0a",
		"/me/a\\b", "/me/a\nb", strings.Repeat("x", maxPath+1),
	} {
		_, err := RelativePath(path, "")
		requireCode(t, err, "workiq_cursor_invalid")
	}
	_, err = RelativePath("/me/mailFolders/other/messages/delta()", scope)
	requireCode(t, err, "workiq_cursor_invalid")
}

func TestWorkIQPageFailureShapes(t *testing.T) {
	for _, raw := range []string{
		`{`, `{"structuredContent":{"results":[]}}`,
		`{"structuredContent":{"results":[{},{}]}}`,
		`{"structuredContent":{"results":[{"statusCode":0,"data":{}}]}}`,
		`{"structuredContent":{"results":[{"statusCode":200,"data":[]}]}}`,
		`{"structuredContent":{"results":[{"statusCode":200,"data":{"@odata.nextLink":"/organization"}}]}}`,
	} {
		page, err := decodePage(context.Background(), []byte(raw))
		if err == nil || len(page.JSON) != 0 {
			t.Fatalf("malformed page admitted: %s %v", page.JSON, err)
		}
	}
	for raw, code := range map[string]string{
		`{"isError":true,"content":[{"type":"text","text":"run workiq auth login test.user@example.invalid"}]}`:       "workiq_signin_required",
		`{"isError":true,"content":[{"type":"text","text":"accept-eula private-download"}]}`:                          "workiq_eula_required",
		`{"isError":true,"content":[{"type":"text","text":"admin consent"}]}`:                                         "workiq_consent_required",
		`{"structuredContent":{"results":[{"statusCode":400,"error":"Access denied policy allowlist","data":null}]}}`: "workiq_policy_denied",
	} {
		page, err := decodePage(context.Background(), []byte(raw))
		requireCode(t, err, code)
		if len(page.JSON) != 0 {
			t.Fatal("server error body retained")
		}
	}
	_, err := decodePage(context.Background(), []byte(strings.Repeat(" ", maxFrame+1)))
	requireCode(t, err, "workiq_page_too_large")
}

func TestWorkIQPageSafeOpaqueDelta(t *testing.T) {
	raw := []byte(`{"structuredContent":{"results":[{"statusCode":200,"error":null,"data":{"value":[],"@odata.deltaLink":"/me/mailFolders/f/messages/delta()?$deltatoken=a%2Fb%2Bz&x=2&x=1"}}]}}`)
	page, err := decodePage(context.Background(), raw)
	if err != nil || page.DeltaCursor != "/me/mailFolders/f/messages/delta()?$deltatoken=a%2Fb%2Bz&x=2&x=1" || !json.Valid(page.JSON) {
		t.Fatalf("opaque safe delta: %+v %v", page, err)
	}
	old := scrubServer
	scrubServer = func([]byte) ([]byte, int) { return []byte(`{`), 1 }
	defer func() { scrubServer = old }()
	page, err = decodePage(context.Background(), raw)
	requireCode(t, err, "workiq_protocol")
	if len(page.JSON) != 0 {
		t.Fatal("invalid scrub output escaped admission")
	}
}
