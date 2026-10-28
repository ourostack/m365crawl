package store

import "testing"

func TestWorkIQSourceKeysNeverFallThroughToTeamsHealth(t *testing.T) {
	for _, tc := range []struct {
		key, family, account string
	}{
		{`workiq:["mail","outlook/Profile"]`, "workiq", "outlook/Profile"},
		{`workiq:["chats","tenant/user"]`, "workiq", "tenant/user"},
		{`workiq:["mail","outlook/Profile","coverage"]`, "workiq", "outlook/Profile"},
		{`workiq:["chats","tenant/user","status"]`, "workiq", "tenant/user"},
		{`workiq:not-json`, "", ""},
		{`workiq:["mail"]`, "", ""},
		{`workiq:["mail","outlook/"]`, "", ""},
		{`workiq:["mail","tenant/user"]`, "", ""},
		{`workiq:["chats","outlook/Profile"]`, "", ""},
		{`workiq:["chats","tenant/user","write"]`, "", ""},
		{`workiq:["unknown","tenant/user"]`, "", ""},
		{`workiq:["mail","outlook/Profile","sync","extra"]`, "", ""},
	} {
		t.Run(tc.key, func(t *testing.T) {
			family, account := SourceFamily(tc.key)
			if family != tc.family || account != tc.account {
				t.Fatal("explicit WorkIQ outcome fell through to Teams or changed its owner")
			}
		})
	}
}

func TestWorkIQOutcomeScopeKeepsProbeKindAndRejectsMalformedOwners(t *testing.T) {
	for _, key := range []string{"teams", `workiq:null`, `workiq:{}`, `workiq:[1,2]`,
		`workiq:["chats","tenant"]`, `workiq:["chats","/user"]`, `workiq:["chats","tenant/"]`,
		`workiq:["chats","tenant/user/extra"]`, `workiq:["mail","outlook/Profile",""]`} {
		family, account, kind, ok := WorkIQSourceScope(key)
		if ok || family != "" || account != "" || kind != "" {
			t.Fatal("invalid source key produced a usable family/owner/kind")
		}
	}
	for _, tc := range []struct{ key, kind string }{
		{`workiq:["mail","outlook/Profile"]`, "sync"},
		{`workiq:["mail","outlook/Profile","sync"]`, "sync"},
		{`workiq:["mail","outlook/Profile","coverage"]`, "coverage"},
		{`workiq:["chats","tenant/user","status"]`, "status"},
	} {
		_, _, kind, ok := WorkIQSourceScope(tc.key)
		if !ok || kind != tc.kind {
			t.Fatal("probe or coverage outcome was relabeled as data sync")
		}
	}
}
