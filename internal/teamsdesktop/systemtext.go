package teamsdesktop

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// systemText is the short text synthesized for a system message, so an agent reads "Call ended ·
// 23m" instead of a blank. The cache holds markup for these (call events, thread activity) or
// bare JSON metadata (call recording and transcript notices), never message text. It returns ""
// for a message that is not a system one, and for Control messages, which carry nothing a reader
// needs.
func systemText(messageType, content string) string {
	switch {
	case messageType == "Event/Call":
		return callEventText(content)
	case strings.HasPrefix(messageType, "ThreadActivity/"):
		return threadActivityText(strings.TrimPrefix(messageType, "ThreadActivity/"))
	}
	return ""
}

var (
	callEndedRe    = regexp.MustCompile(`<ended\s*/?>`)
	callEventRe    = regexp.MustCompile(`<callEventType>\s*([A-Za-z]*)\s*</callEventType>`)
	callDurationRe = regexp.MustCompile(`<duration>\s*(\d+)\s*</duration>`)
)

// callEventText reads an Event/Call body: the event (the <ended/> marker or callEventType),
// whether it is a meeting (meetingDetails present), and the longest participant duration, which
// is the call's length in seconds.
func callEventText(content string) string {
	if strings.TrimSpace(content) == "" {
		return "Call event"
	}
	kind := "Call"
	if strings.Contains(content, "<meetingDetails") {
		kind = "Meeting"
	}
	state := "started"
	if m := callEventRe.FindStringSubmatch(content); callEndedRe.MatchString(content) || (m != nil && strings.EqualFold(m[1], "callEnded")) {
		state = "ended"
	}
	out := kind + " " + state
	var longest int64
	for _, m := range callDurationRe.FindAllStringSubmatch(content, -1) {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && n > longest {
			longest = n
		}
	}
	if d := formatDuration(longest); state == "ended" && d != "" {
		out += " · " + d
	}
	return out
}

// formatDuration renders seconds as "45s", "23m", "1h 5m" or "2h"; 0 gives "".
func formatDuration(secs int64) string {
	switch {
	case secs <= 0:
		return ""
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	}
	h, m := secs/3600, secs%3600/60
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

var threadActivityLabels = map[string]string{
	"AddMember":            "Member added",
	"DeleteMember":         "Member removed",
	"MemberJoined":         "Member joined",
	"MemberLeft":           "Member left",
	"TopicUpdate":          "Topic updated",
	"TopicTopicUpdated":    "Topic updated",
	"PinnedItemsUpdate":    "Pinned items updated",
	"AddCustomApp":         "App added",
	"DeleteCustomApp":      "App removed",
	"MeetingPolicyUpdated": "Meeting policy updated",
}

// threadActivityText labels a ThreadActivity/<Kind> message; an unknown kind is its name split
// into lower-case words.
func threadActivityText(kind string) string {
	if l, ok := threadActivityLabels[kind]; ok {
		return l
	}
	if kind == "" {
		return "Thread activity"
	}
	var words []rune
	for i, r := range kind {
		if i > 0 && r >= 'A' && r <= 'Z' {
			words = append(words, ' ')
		}
		words = append(words, r)
	}
	return "Thread activity: " + strings.ToLower(string(words))
}

// mediaMetadataText is the notice for a call recording or transcript message whose content is
// bare JSON metadata (the real cache escapes its quotes, so it is not valid JSON; the leading
// brace is the test). Other content, such as a recording's markup, is real text and returns "".
func mediaMetadataText(messageType, content string) string {
	if !strings.HasPrefix(strings.TrimSpace(content), "{") {
		return ""
	}
	switch {
	case strings.Contains(messageType, "Transcript"):
		return "Call transcript available"
	case strings.Contains(messageType, "Recording"):
		return "Call recording available"
	}
	return ""
}
