package teamsdesktop

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

// ReactionActor names who reacted, for an activity item about a reaction. The feed item carries
// only the reaction's key and time; the reactors are on the reacted-to message. raw is that
// message's canonical JSON, key the reaction key (the item's subtype) and at the item's time.
// Among the users who used that key it picks the one whose reaction time is nearest to at,
// skipping self (the account's own reactions never reach its feed), and returns their MRI. It
// returns "" when the message names no such user.
func ReactionActor(raw []byte, key string, at time.Time, self string) string {
	var doc struct {
		Properties map[string]any `json:"properties"`
	}
	if key == "" || json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	list := jsonList(doc.Properties["emotions"])
	if list == nil {
		list = jsonList(doc.Properties["deltaEmotions"])
	}
	best, bestDelta := "", math.Inf(1)
	for _, e := range list {
		emotion, _ := e.(map[string]any)
		if k, _ := emotion["key"].(string); !strings.EqualFold(k, key) {
			continue
		}
		users, _ := emotion["users"].([]any)
		for _, u := range users {
			user, _ := u.(map[string]any)
			mri, _ := user["mri"].(string)
			if mri == "" || strings.EqualFold(mri, self) {
				continue
			}
			delta := math.MaxFloat64 // a user with no time loses to any user with one
			if ms, ok := user["time"].(float64); ok {
				delta = math.Abs(ms - float64(at.UnixMilli()))
			}
			if best == "" || delta < bestDelta {
				best, bestDelta = mri, delta
			}
		}
	}
	return best
}

// jsonList is v as a JSON array: a native array, or a string holding one (the cache stores
// emotions both ways).
func jsonList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case string:
		var out []any
		if json.Unmarshal([]byte(x), &out) == nil {
			return out
		}
	}
	return nil
}
