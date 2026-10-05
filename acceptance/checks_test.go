//go:build acceptance

package acceptance

import (
	"strings"
	"testing"
)

func TestCredentialShapesIgnoreBareWords(t *testing.T) {
	for name, value := range map[string]string{
		"claims request":     `{"claims":"{\"access_token\":{\"optional\":true}}"}`,
		"escaped claims":     `"{\"access_token\":{\"xms_cc\":{\"values\":[\"cp1\"]}}}"`,
		"prose access_token": `{"description":"Uses the access_token to call the service on your behalf."}`,
		"prose refresh":      `{"description":"Stores the refresh_token securely and never shares it."}`,
		"prose bearer":       `{"description":"Sends a Bearer header with each request to the bot."}`,
		"word at end":        `{"note":"access_token"}`,
		"short value":        `{"access_token":"abc123"}`,
		"bearer short":       `Bearer abc`,
	} {
		if n := credentialShapes(value); n != 0 {
			t.Errorf("%s: %d credential-shaped strings, want 0", name, n)
		}
	}
}

func TestCredentialShapesCatchRealLookingTokens(t *testing.T) {
	secret := strings.Repeat("aB3-_x.Y+/=", 3)  // 33 characters of the token alphabet
	for name, value := range map[string]string{ //nolint:gosec // synthetic strings shaped like credentials, to prove the check detects them
		"json access_token":         `{"access_token":"` + secret + `"}`,
		"json refresh_token":        `{"refresh_token":"` + secret + `"}`,
		"escaped quotes":            `"{\"access_token\":\"` + secret + `\"}"`,
		"double escaped":            `"{\\\"refresh_token\\\":\\\"` + secret + `\\\"}"`,
		"equals form":               `access_token=` + secret + `&x=1`,
		"colon space form":          `refresh_token: ` + secret,
		"single quotes":             `{'access_token': '` + secret + `'}`,
		"bearer header":             `Authorization: Bearer ` + secret,
		"bearer exactly 20":         `Bearer ` + strings.Repeat("a", 20),
		"access_token exactly 20":   `access_token=` + strings.Repeat("Z", 20),
		"three-segment bearer jwt":  `eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4eHh4eHh4eCJ9.c2lnbmF0dXJl`,
		"jwt without any field key": `[ "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4eHh4eHh4eCJ9.c2lnbmF0dXJl" ]`,
	} {
		if n := credentialShapes(value); n == 0 {
			t.Errorf("%s: not detected", name)
		}
	}
	if n := credentialShapes(`Bearer ` + strings.Repeat("a", 19)); n != 0 {
		t.Errorf("19-character run after Bearer counted: %d", n)
	}
}

func TestConversationAccounting(t *testing.T) {
	// base is a consistent snapshot: 10 versions, 8 keys (2 superseded), 1 tombstoned, 7 live and mapped.
	base := func() (refCounts, ours) {
		var rc refCounts
		rc.Conversations.RecordVersions, rc.Conversations.DistinctKeys = 10, 8
		rc.Conversations.LatestLive, rc.Conversations.LatestTombstoned = 7, 1
		live := []string{"a", "b", "c", "d", "e", "f", "g"}
		rc.Hashes.Conversations = live
		return rc, ours{rawVersions: 10, convIDs: toSet(live), omissions: map[string]int{}}
	}
	t.Run("consistent", func(t *testing.T) {
		rc, us := base()
		if problems, _ := conversationAccounting(rc, us); len(problems) != 0 {
			t.Errorf("unexpected problems: %v", problems)
		}
	})
	t.Run("record the reference cannot decode is allowed and reported", func(t *testing.T) {
		rc, us := base()
		rc.Undecodable.Conversations = []string{"z"}
		rc.Undecodable.ConversationVersions = 1
		us.rawVersions, us.convIDs["z"] = 11, true
		problems, notes := conversationAccounting(rc, us)
		if len(problems) != 0 {
			t.Errorf("unexpected problems: %v", problems)
		}
		if !strings.Contains(strings.Join(notes, "\n"), "of which in 1 records the reference could not decode: 1") {
			t.Errorf("allowance not reported: %v", notes)
		}
	})
	t.Run("extra version nobody explains fails", func(t *testing.T) {
		rc, us := base()
		us.rawVersions = 11
		if problems, _ := conversationAccounting(rc, us); len(problems) == 0 {
			t.Error("an unexplained extra record version passed")
		}
	})
	t.Run("mapped conversation outside the reference undecodable list fails", func(t *testing.T) {
		rc, us := base()
		us.convIDs["z"] = true
		if problems, _ := conversationAccounting(rc, us); len(problems) == 0 {
			t.Error("a conversation absent from the reference, not in its undecodable list, passed")
		}
	})
	t.Run("dropped reference conversation fails", func(t *testing.T) {
		rc, us := base()
		delete(us.convIDs, "g")
		if problems, _ := conversationAccounting(rc, us); len(problems) == 0 {
			t.Error("a dropped reference conversation passed")
		}
	})
	t.Run("dropped conversation fails even with an allowance", func(t *testing.T) {
		rc, us := base()
		rc.Undecodable.Conversations = []string{"z"}
		rc.Undecodable.ConversationVersions = 1
		us.rawVersions, us.convIDs["z"] = 11, true
		delete(us.convIDs, "g")
		if problems, _ := conversationAccounting(rc, us); len(problems) == 0 {
			t.Error("a dropped reference conversation passed next to an allowance")
		}
	})
}
