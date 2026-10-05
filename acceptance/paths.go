// Helpers shared by the real-cache acceptance tests. They need neither the cache nor the
// acceptance build tag, so CI exercises them.
package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// knownFields are the field names of the Teams records and of the canonical encoding. A mismatch
// path prints only these: any other object key may be data (an id, a name used as a key), so it
// prints as <field>.
var knownFields = map[string]bool{
	"activityId":          true,
	"activitySubtype":     true,
	"activityType":        true,
	"chatTitle":           true,
	"clientArrivalTime":   true,
	"clientMessageId":     true,
	"content":             true,
	"contentType":         true,
	"conversationId":      true,
	"creator":             true,
	"creatorId":           true,
	"deletetime":          true,
	"deleteTime":          true,
	"deletionInfo":        true,
	"deltaEmotions":       true,
	"displayName":         true,
	"edittime":            true,
	"emotions":            true,
	"fileName":            true,
	"files":               true,
	"fileType":            true,
	"friendlyName":        true,
	"from":                true,
	"id":                  true,
	"imDisplayName":       true,
	"importance":          true,
	"isRead":              true,
	"key":                 true,
	"lastMessageTimeUtc":  true,
	"links":               true,
	"longTitle":           true,
	"members":             true,
	"mentions":            true,
	"messageId":           true,
	"messageMap":          true,
	"messageType":         true,
	"mri":                 true,
	"objectUrl":           true,
	"openUrl":             true,
	"originalArrivalTime": true,
	"parentId":            true,
	"parentMessageId":     true,
	"pinned":              true,
	"pinnedTime":          true,
	"properties":          true,
	"replyChainId":        true,
	"shortTitle":          true,
	"sourceMessageId":     true,
	"sourceReplyChainId":  true,
	"sourceThreadId":      true,
	"spaceThreadTopic":    true,
	"subject":             true,
	"teamId":              true,
	"teamsAppId":          true,
	"threadProperties":    true,
	"timestamp":           true,
	"title":               true,
	"topic":               true,
	"topicThreadTopic":    true,
	"type":                true,
	"url":                 true,
	"users":               true,
	"version":             true,
	"$array":              true,
	"$props":              true,
	"$map":                true,
	"$set":                true,
	"$date":               true,
	"$bytes":              true,
	"$error":              true,
	"$wrapper":            true,
	"$regexp":             true,
	"$bigint":             true,
	"$number":             true,
	"$undefined":          true,
	"$hole":               true,
	"$cycle":              true,
	"name":                true,
	"message":             true,
	"stack":               true,
	"cause":               true,
}

func safeKey(k string) string {
	if knownFields[k] {
		return k
	}
	return "<field>"
}

type tokEvent struct {
	text string // token text; for a delimiter the delimiter
	path string // path of the value or key the token belongs to
	key  bool
	end  bool
}

// tokens streams a canonical JSON document as events with field paths.
type tokenizer struct {
	dec   *json.Decoder
	stack []frame
}

type frame struct {
	obj       bool
	expectKey bool
	key       string
}

func newTokenizer(s string) *tokenizer {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	return &tokenizer{dec: d}
}

func (z *tokenizer) path() string {
	var b strings.Builder
	for _, f := range z.stack {
		if f.obj {
			if f.key != "" {
				b.WriteString("." + safeKey(f.key))
			}
		} else {
			b.WriteString("[]")
		}
	}
	if b.Len() == 0 {
		return "$"
	}
	return "$" + b.String()
}

// valueDone marks the value just finished in the enclosing container.
func (z *tokenizer) valueDone() {
	if n := len(z.stack); n > 0 && z.stack[n-1].obj {
		z.stack[n-1].expectKey = true
	}
}

func (z *tokenizer) next() (tokEvent, bool) {
	tok, err := z.dec.Token()
	if err != nil {
		return tokEvent{}, false
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{', '[':
			ev := tokEvent{text: string(v), path: z.path()}
			z.stack = append(z.stack, frame{obj: v == '{', expectKey: v == '{'})
			return ev, true
		default:
			z.stack = z.stack[:len(z.stack)-1]
			ev := tokEvent{text: string(v), path: z.path(), end: true}
			z.valueDone()
			return ev, true
		}
	case string:
		if n := len(z.stack); n > 0 && z.stack[n-1].obj && z.stack[n-1].expectKey {
			z.stack[n-1].key, z.stack[n-1].expectKey = v, false
			return tokEvent{text: v, path: z.path(), key: true}, true
		}
		ev := tokEvent{text: "s:" + v, path: z.path()}
		z.valueDone()
		return ev, true
	default:
		ev := tokEvent{text: fmt.Sprintf("%T:%v", v, v), path: z.path()}
		z.valueDone()
		return ev, true
	}
}

// firstDifference returns the field path of the first token where two canonical documents
// differ, with the kind of difference. It never returns a value.
func firstDifference(a, b string) string {
	za, zb := newTokenizer(a), newTokenizer(b)
	for {
		ea, oka := za.next()
		eb, okb := zb.next()
		switch {
		case !oka && !okb:
			return "$ [documents differ, tokens equal]"
		case !oka || !okb:
			return "$ [one document ends early]"
		case ea.text == eb.text && ea.path == eb.path:
			continue
		case ea.key || eb.key:
			return ea.path + " [key]"
		case ea.end != eb.end || (len(ea.text) > 0 && len(eb.text) > 0 && (ea.text[0] == '{' || ea.text[0] == '[' || eb.text[0] == '{' || eb.text[0] == '[')):
			return ea.path + " [shape]"
		default:
			return ea.path + " [value]"
		}
	}
}

// stderrNote saves a subprocess's stderr to a file in the test's temp directory and returns a
// description with only the byte count and the file path: stderr can quote record keys or
// content, so it never goes into a failure message.
func stderrNote(t *testing.T, name string, stderr []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".stderr")
	if err := os.WriteFile(path, stderr, 0o600); err != nil {
		return fmt.Sprintf("%d bytes of stderr (not saved: %v)", len(stderr), err)
	}
	return fmt.Sprintf("%d bytes of stderr saved to %s", len(stderr), path)
}

var (
	credentialJWT    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)
	credentialBearer = regexp.MustCompile(`(?i)\bbearer\s+(\S{16,})`)
)

var credentialKeys = map[string]bool{
	"access_token":  true,
	"refresh_token": true,
	"id_token":      true,
	"authorization": true,
	"client_secret": true,
	"password":      true,
}

var credentialPrefilterTerms = []string{
	"eyJ",
	"Bearer ",
	"bearer ",
	"access_token",
	"refresh_token",
	"id_token",
	"authorization",
	"client_secret",
	"password",
}

func credentialLeakCandidateSQL() string {
	terms := slices.Clone(credentialPrefilterTerms)
	likes := make([]string, 0, len(terms))
	for _, term := range terms {
		likes = append(likes, fmt.Sprintf("value_json like '%%%s%%'", term))
	}
	return "select source, database, store, value_json from records where " + strings.Join(likes, " or ")
}

func credentialLeakCount(valueJSON string) int {
	var v any
	if err := json.Unmarshal([]byte(valueJSON), &v); err != nil {
		return 0
	}
	return credentialLeakValueCount(v, 0)
}

func credentialLeakValueCount(v any, depth int) int {
	switch x := v.(type) {
	case map[string]any:
		n := 0
		for k, child := range x {
			if credentialKeys[strings.ToLower(k)] {
				n += keyedCredentialLeakCount(child)
			}
			n += credentialLeakValueCount(child, depth)
		}
		return n
	case []any:
		n := 0
		for _, child := range x {
			n += credentialLeakValueCount(child, depth)
		}
		return n
	case string:
		n := len(credentialJWT.FindAllStringIndex(x, -1))
		for _, m := range credentialBearer.FindAllStringSubmatch(x, -1) {
			if len(m) < 2 || credentialJWT.MatchString(m[1]) {
				continue
			}
			n++
		}
		if depth < 4 {
			s := strings.TrimSpace(x)
			if s != "" && (s[0] == '{' || s[0] == '[') {
				var inner any
				if json.Unmarshal([]byte(s), &inner) == nil {
					n += credentialLeakValueCount(inner, depth+1)
				}
			}
		}
		return n
	default:
		return 0
	}
}

func isRedactedCredentialValue(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "[redacted]", "******":
		return true
	default:
		return false
	}
}

func keyedCredentialLeakCount(v any) int {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	s = strings.TrimSpace(s)
	if s == "" || isRedactedCredentialValue(s) || isRedactedAuthorizationValue(s) {
		return 0
	}
	return 1
}

func isRedactedAuthorizationValue(s string) bool {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "authorization:") {
		s = strings.TrimSpace(s[len("authorization:"):])
		lower = strings.ToLower(s)
	}
	if isRedactedCredentialValue(s) {
		return true
	}
	if !strings.HasPrefix(lower, "bearer ") {
		return false
	}
	return isRedactedCredentialValue(strings.TrimSpace(s[len("bearer "):]))
}
