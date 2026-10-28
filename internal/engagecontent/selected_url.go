package engagecontent

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

type ThreadTarget struct {
	URL, ThreadID, GroupID string
}

func ParseThreadURL(raw string) (ThreadTarget, error) {
	bad := &ReadError{Code: "invalid_target"}
	if len(raw) > 16384 || strings.Contains(raw, "#") {
		return ThreadTarget{}, bad
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawPath != "" || u.Opaque != "" || u.ForceQuery {
		return ThreadTarget{}, bad
	}
	path, group := u.Path, ""
	switch u.Host {
	case "engage.cloud.microsoft", "engage.cloud.microsoft:443":
		if u.RawQuery != "" || !strings.HasPrefix(path, "/main/threads/") {
			return ThreadTarget{}, bad
		}
		path = strings.TrimPrefix(path, "/main")
	case "teams.microsoft.com", "teams.microsoft.com:443":
		if path != "/l/entity/683f3525-d193-4a67-8d91-22093beab1ca/" {
			return ThreadTarget{}, bad
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(query) != 1 || len(query["context"]) != 1 {
			return ThreadTarget{}, bad
		}
		context, err := selectedStrings(query.Get("context"), "internalId", "contextType", "subEntityId")
		if err != nil || context["contextType"] != "engageCommunity" {
			return ThreadTarget{}, bad
		}
		internal := context["internalId"]
		if !strings.HasPrefix(internal, "19:") || !strings.HasSuffix(internal, "@EngageCommunity") {
			return ThreadTarget{}, bad
		}
		group = strings.TrimSuffix(strings.TrimPrefix(internal, "19:"), "@EngageCommunity")
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(group)
		if err != nil || len(group) > 1024 || len(decoded) == 0 || !json.Valid(decoded) {
			return ThreadTarget{}, bad
		}
		groupIdentity, err := selectedStrings(string(decoded), "_type", "id")
		if err != nil || groupIdentity["_type"] != "Group" || !selectedDecimal.MatchString(groupIdentity["id"]) {
			return ThreadTarget{}, bad
		}
		nested, err := selectedStrings(context["subEntityId"], "deepLinkType", "path")
		if err != nil || nested["deepLinkType"] != "crossapp" {
			return ThreadTarget{}, bad
		}
		path = nested["path"]
	default:
		return ThreadTarget{}, bad
	}
	if !strings.HasPrefix(path, "/threads/") {
		return ThreadTarget{}, bad
	}
	id := strings.TrimPrefix(path, "/threads/")
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(id)
	if err != nil || len(id) > 1024 || len(decoded) > 512 {
		return ThreadTarget{}, bad
	}
	identity, err := selectedStrings(string(decoded), "_type", "id")
	if err != nil || identity["_type"] != "Thread" || !selectedDecimal.MatchString(identity["id"]) {
		return ThreadTarget{}, bad
	}
	return ThreadTarget{"https://engage.cloud.microsoft/main/threads/" + id, id, group}, nil
}

var selectedDecimal = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var selectedURLs = regexp.MustCompile(`(?i)(?:[a-z][a-z0-9+.-]*://|//|javascript:|data:)[^\s<>"]+`)

func selectedStrings(raw string, keys ...string) (map[string]string, error) {
	bad := &ReadError{Code: "invalid_target"}
	if len(raw) > 8192 {
		return nil, bad
	}
	d := json.NewDecoder(strings.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, bad
	}
	out := map[string]string{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, bad
		}
		// In an object, Decoder.Token returns either a string key or an error.
		key := token.(string)
		if _, exists := out[key]; exists {
			return nil, bad
		}
		var value string
		if d.Decode(&value) != nil {
			return nil, bad
		}
		out[key] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, bad
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF || len(out) != len(keys) {
		return nil, bad
	}
	for _, key := range keys {
		if out[key] == "" {
			return nil, bad
		}
	}
	return out, nil
}

// SafeThreadLink admits data links only, never acquisition or download authority.
func SafeThreadLink(raw string) bool {
	if len(raw) > 262144 || strings.ContainsAny(raw, "\x00\r\n\t") {
		return false
	}
	decoded, decodeErr := url.QueryUnescape(raw)
	if decodeErr != nil || strings.ContainsAny(decoded, "\x00\r\n\t") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return false
	}
	parts := strings.ToLower(u.Path + "?" + u.RawQuery + "#" + u.Fragment)
	if strings.Contains(parts, "tempauth") || strings.Contains(parts, "downloadurl") {
		return false
	}
	for _, values := range []string{u.RawQuery, u.Fragment} {
		query, err := url.ParseQuery(values)
		if err != nil {
			return false
		}
		for key := range query {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			if strings.Contains(key, "%") || teamsdesktop.Denied(key) || normalized == "sig" || normalized == "signature" || normalized == "authkey" || normalized == "code" || normalized == "key" || normalized == "downloadurl" {
				return false
			}
			for _, value := range query[key] {
				if selectedURLs.MatchString(value) {
					return false
				}
			}
		}
	}
	encoded, _ := json.Marshal(raw)
	_, changes := teamsdesktop.Scrub(encoded)
	return changes == 0
}

func scrubSelectedText(raw string) (string, bool) {
	changed := false
	raw = selectedURLs.ReplaceAllStringFunc(raw, func(value string) string {
		if SafeThreadLink(value) {
			return value
		}
		changed = true
		return "[redacted]"
	})
	encoded, _ := json.Marshal(raw)
	clean, n := teamsdesktop.Scrub(encoded)
	var out string
	_ = json.Unmarshal(clean, &out)
	return out, changed || n > 0
}
