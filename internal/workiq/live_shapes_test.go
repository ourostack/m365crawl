package workiq

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// These envelopes reproduce shapes the WorkIQ server returned during live qualification
// (2026-10-29, through the MCP connector's fetch rather than the stdio executable).

func TestWorkIQMailFolderKeySegmentIsTheOnlyCursorRewrite(t *testing.T) {
	for raw, want := range map[string]string{ //nolint:gosec // G101: synthetic cursor paths with placeholder tokens, not credentials.
		"/me/mailFolders('inbox')/messages?%24select=id%2cfrom&%24top=50&%24skip=50":        "/me/mailFolders/inbox/messages?%24select=id%2cfrom&%24top=50&%24skip=50",
		"/me/mailFolders('AQMk%3D%3D')/messages/delta?$skiptoken=a%2Fb('x')&$top=1":         "/me/mailFolders/AQMk%3D%3D/messages/delta?$skiptoken=a%2Fb('x')&$top=1",
		"/me/mailFolders('f-1')/messages/delta?$deltatoken=z":                               "/me/mailFolders/f-1/messages/delta?$deltatoken=z",
		"https://graph.microsoft.com/v1.0/me/mailFolders('f')/messages/delta?$deltatoken=z": "https://graph.microsoft.com/v1.0/me/mailFolders/f/messages/delta?$deltatoken=z",
		"/me/mailFolders('f')":                           "/me/mailFolders/f",
		"/me/mailFolders/f/messages/delta?$deltatoken=z": "/me/mailFolders/f/messages/delta?$deltatoken=z",
		"/me/mailFolders('')/messages/delta":             "/me/mailFolders('')/messages/delta",
		"/me/mailFolders('a'b')/messages/delta":          "/me/mailFolders('a'b')/messages/delta",
		"/me/mailFolders('a/b')/messages/delta":          "/me/mailFolders('a/b')/messages/delta",
		"/me/mailFolders('f')x/messages":                 "/me/mailFolders('f')x/messages",
		"/me/mailFolders('f'/messages":                   "/me/mailFolders('f'/messages",
		"/me/mailfolders('f')/messages":                  "/me/mailfolders('f')/messages",
		"/me/messages('m')/attachments":                  "/me/messages('m')/attachments",
		"/me/mailFolders/f/childFolders('c')/messages":   "/me/mailFolders/f/childFolders('c')/messages",
		"/chats/c/messages?$skiptoken=mailFolders('x')":  "/chats/c/messages?$skiptoken=mailFolders('x')",
	} {
		if got := mailFolderCursor(raw); got != want {
			t.Errorf("mailFolderCursor(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestWorkIQDecodedMailCursorsUseTheAcceptedSegment(t *testing.T) {
	raw := []byte(`{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[],` +
		`"@odata.nextLink":"/me/mailFolders('f')/messages/delta?$skiptoken=n%24x",` +
		`"@odata.deltaLink":"/me/mailFolders('f')/messages/delta?$deltatoken=d%2Bz"}}]}}`)
	page, err := DecodePage(context.Background(), raw)
	if err != nil || page.NextCursor != "/me/mailFolders/f/messages/delta?$skiptoken=n%24x" ||
		page.DeltaCursor != "/me/mailFolders/f/messages/delta?$deltatoken=d%2Bz" {
		t.Fatalf("cursors = %q %q %v", page.NextCursor, page.DeltaCursor, err)
	}
	if _, err := RelativePath(page.DeltaCursor, "/me/mailFolders/f/messages/delta"); err != nil {
		t.Fatal("rewritten cursor left its folder scope", err)
	}
	if _, err := RelativePath(page.DeltaCursor, "/me/mailFolders/other/messages/delta"); err == nil {
		t.Fatal("rewritten cursor escaped scope binding")
	}
}

func TestWorkIQThrottleWaitComesFromTheResult(t *testing.T) {
	const throttled = `{"isError":true,"structuredContent":{"results":[{"data":null,"error":{"error":{"code":"TooManyRequests","message":"Too many requests. Please try again later.","innerError":{"date":"2026-10-29T23:33:00"}}},%s"statusCode":%d}]}}`
	for _, tc := range []struct {
		field  string
		status int
		want   string
	}{
		{`"retryAfterSeconds":12,`, 429, "12"},
		{`"retryAfterSeconds":1.2,`, 429, "2"},
		{`"retryAfterSeconds":0,`, 429, "0"},
		{`"retryAfterSeconds":7,`, 503, "7"},
		{``, 429, ""},
		{`"retryAfterSeconds":null,`, 429, ""},
		{`"retryAfterSeconds":-1,`, 429, ""},
		{`"retryAfterSeconds":"12",`, 429, ""},
		{`"retryAfterSeconds":1e12,`, 429, ""},
	} {
		raw := fmt.Sprintf(throttled, tc.field, tc.status)
		for _, envelope := range []string{raw, strings.Replace(raw, `"isError":true,`, ``, 1)} {
			page, err := decodePage(context.Background(), []byte(envelope))
			if err == nil || page.StatusCode != tc.status || page.RetryAfter != tc.want || len(page.JSON) != 0 {
				t.Fatalf("%s: page %+v err %v", envelope, page, err)
			}
		}
	}
}

func TestWorkIQPathRefusalIsAFixedCodeWithoutTheEchoedPath(t *testing.T) {
	const secret = "/me/mailFolders('inbox')/messages?%24select=id&%24skip=50&$skiptoken=private-cursor"
	for _, raw := range []string{
		`{"isError":true,"structuredContent":{"results":[{"data":null,"error":"Access denied for GET path: ` + secret + `","statusCode":400}]}}`,
		`{"structuredContent":{"results":[{"data":null,"error":"Access denied for GET path: ` + secret + `","statusCode":400}]}}`,
	} {
		page, err := decodePage(context.Background(), []byte(raw))
		requireCode(t, err, "workiq_policy_denied")
		if page.StatusCode != 400 || len(page.JSON) != 0 || page.RetryAfter != "" ||
			strings.Contains(err.Error(), "private-cursor") || strings.Contains(err.Error(), "mailFolders") {
			t.Fatalf("refusal leaked or lost its status: %+v %v", page, err)
		}
	}
	// A one-result isError envelope without a failed result keeps the original classification.
	_, err := decodePage(context.Background(), []byte(`{"isError":true,"structuredContent":{"results":[{"data":{},"statusCode":200}]}}`))
	requireCode(t, err, "workiq_failed")
}
