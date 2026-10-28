package engagecontent

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func selectedTestID() string {
	return base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Thread","id":"42"}`))
}

func TestSelectedURLAdmission(t *testing.T) {
	id := selectedTestID()
	canonical := "https://engage.cloud.microsoft/main/threads/" + id
	target, err := ParseThreadURL(canonical)
	if err != nil || target.URL != canonical || target.ThreadID != id || target.GroupID != "" {
		t.Fatal("canonical thread not admitted", err)
	}
	group := base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Group","id":"7"}`))
	context := `{"internalId":"19:` + group + `@EngageCommunity","contextType":"engageCommunity","subEntityId":"{\"deepLinkType\":\"crossapp\",\"path\":\"/threads/` + id + `\"}"}`
	deep := "https://teams.microsoft.com/l/entity/683f3525-d193-4a67-8d91-22093beab1ca/?context=" + url.QueryEscape(context)
	target, err = ParseThreadURL(deep)
	if err != nil || target.URL != canonical || target.GroupID != group {
		t.Fatal("Teams thread not admitted", err)
	}
	for _, bad := range []string{
		"", "http://engage.cloud.microsoft/main/threads/" + id,
		"https://user@engage.cloud.microsoft/main/threads/" + id,
		canonical + "?sig=secret", canonical + "#", canonical + "/", canonical + "?",
		strings.Replace(canonical, ".microsoft/", ".microsoft:8443/", 1),
		strings.Replace(canonical, "/main/", "/other/", 1),
		strings.Replace(canonical, id, "bad", 1),
		strings.Replace(canonical, id, base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"User","id":"42"}`)), 1),
		strings.Replace(canonical, id, base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Thread","id":"042"}`)), 1),
		strings.Replace(canonical, id, base64.RawURLEncoding.EncodeToString([]byte(`{"_type":"Thread","id":"42","id":"43"}`)), 1),
		deep + "&context=" + url.QueryEscape(context), deep + "&other=x",
		strings.Replace(deep, "teams.microsoft.com", "example.test", 1),
		strings.Replace(deep, "/l/entity/", "/l/other/", 1),
		strings.Replace(deep, url.QueryEscape("crossapp"), "wrong", 1),
		"https://teams.microsoft.com/l/entity/683f3525-d193-4a67-8d91-22093beab1ca/?context=%ZZ",
		canonical + strings.Repeat("x", 17000),
	} {
		if _, err := ParseThreadURL(bad); err == nil {
			t.Fatal("unsafe request admitted")
		}
	}
}

func TestSelectedValueSafety(t *testing.T) {
	for _, raw := range []string{"https://example.test/a?x=1", "http://example.test/a"} {
		if !SafeThreadLink(raw) {
			t.Fatal("ordinary link refused")
		}
	}
	for _, raw := range []string{"", "//example.test/a", "javascript:alert(1)", "file:///a", "https://user:pass@example.test/a",
		"https://example.test/a?sig=private", "https://example.test/a?s%69g=private", "https://example.test/a?tempauth=private",
		"https://example.test/a?downloadUrl=private", "https://example.test/_api/tempauth/private", "https://example.test/a?access_token=private",
		"https://example.test/a?authkey=private", "https://example.test/a#sig=private", "https://example.test/%ZZ",
		"https://example.test/a?s%2569g=private", "https://example.test/a?next=https%3A%2F%2Fexample.test%2Fa%3Fsig%3Dprivate",
		"https://example.test/%00", "https://example.test/a?q=%0A"} {
		if SafeThreadLink(raw) {
			t.Fatal("unsafe link retained")
		}
	}
	got, changed := scrubSelectedText("before https://example.test/a?sig=private after https://example.test/safe")
	if !changed || strings.Contains(got, "private") || !strings.Contains(got, "https://example.test/safe") {
		t.Fatal("body link safety not enforced")
	}
	got, changed = scrubSelectedText("safe text")
	if changed || got != "safe text" {
		t.Fatal("safe text changed")
	}
}
