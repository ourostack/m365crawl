package store

import (
	"encoding/json"
	"strings"
)

// WorkIQSourceScope distinguishes data outcomes from counts/probes before any
// legacy unknown-source fallback. Account identifiers are retained verbatim.
func WorkIQSourceScope(key string) (family, account, kind string, ok bool) {
	raw, found := strings.CutPrefix(key, "workiq:")
	if !found {
		return "", "", "", false
	}
	var fields []string
	if json.Unmarshal([]byte(raw), &fields) != nil || len(fields) < 2 || len(fields) > 3 {
		return "", "", "", false
	}
	family, account, kind = fields[0], fields[1], "sync"
	if len(fields) == 3 {
		kind = fields[2]
	}
	if kind != "sync" && kind != "coverage" && kind != "status" {
		return "", "", "", false
	}
	switch family {
	case "mail":
		if !strings.HasPrefix(account, "outlook/") || len(account) == len("outlook/") {
			return "", "", "", false
		}
	case "chats":
		tenant, user, split := strings.Cut(account, "/")
		if !split || tenant == "" || user == "" || strings.HasPrefix(account, "outlook/") || strings.Contains(user, "/") {
			return "", "", "", false
		}
	default:
		return "", "", "", false
	}
	return family, account, kind, true
}
