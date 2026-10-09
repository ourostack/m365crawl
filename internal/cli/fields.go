package cli

import (
	"io"
	"reflect"
	"strings"
)

// fieldSet is one list of --fields keys a command accepts. when names the flag that selects it,
// empty for the command's plain result.
type fieldSet struct {
	when string
	keys func() []string
}

func typeKeys[T any]() []string { return jsonKeys(reflect.TypeFor[T]()) }

// commandFields is the one source of the --fields keys of every list command: the validators read
// it and each command's --help prints it, so the help cannot drift from what a command accepts.
var commandFields = map[string][]fieldSet{
	"search":           {{keys: searchKeys}},
	"messages":         {{keys: typeKeys[messageItem]}},
	"conversations":    {{keys: typeKeys[conversationItem]}},
	"teams":            {{keys: typeKeys[teamItem]}},
	"people":           {{keys: typeKeys[personItem]}},
	"activity":         {{keys: typeKeys[activityItem]}},
	"stores":           {{keys: typeKeys[storeItem]}},
	"records":          {{keys: typeKeys[recordItem]}},
	"unread":           {{keys: typeKeys[messageItem]}, {when: "--by-conversation", keys: typeKeys[unreadConversationItem]}},
	"thread":           {{keys: typeKeys[messageItem]}},
	"watch":            {{keys: watchKeys}},
	"calendar":         {{keys: typeKeys[calendarItem]}},
	"calendar event":   {{keys: eventKeys}},
	"calendar actions": {{keys: typeKeys[actionItem]}},
	"calendar sources": {{keys: typeKeys[calendarSourceItem]}},
	"mail list":        {{keys: listKeys}},
	"mail show":        {{keys: showKeys}},
	"mail thread":      {{keys: listKeys}},
	"mail folders":     {{keys: folderKeys}},
	"mail unread":      {{keys: listKeys}},
	"transcripts":      {{keys: typeKeys[transcriptItem]}},
	"transcripts show": {{keys: typeKeys[transcriptShow]}},
}

// fieldsOf is the --fields keys cmd accepts in the mode when names ("" for its plain result).
func fieldsOf(cmd, when string) []string {
	for _, s := range commandFields[cmd] {
		if s.when == when {
			return s.keys()
		}
	}
	return nil
}

// checkCommandFields rejects a --fields key that cmd does not accept in the mode when names.
func checkCommandFields(rt *runtime, cmd, when string) error {
	return checkKeys(rt, fieldsOf(cmd, when))
}

// watchKeys are the keys of a watch event: those of a message and of an activity item.
func watchKeys() []string {
	return dedupe(append(typeKeys[messageItem](), typeKeys[activityItem]()...))
}

// helpWidth is the width the --fields section of a command's help is wrapped to.
const helpWidth = 80

// writeFieldsHelp prints the --fields keys cmd accepts, after the rest of its help. A command
// without --fields keys prints nothing.
func writeFieldsHelp(w io.Writer, cmd string) {
	sets := commandFields[cmd]
	if len(sets) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("\n--fields keys (comma separated; any other key is a usage error):\n")
	for _, s := range sets {
		lead := ""
		if s.when != "" {
			lead = "With " + s.when + ": "
		}
		b.WriteString(wrapList(lead, s.keys()))
	}
	if cmd == "calendar" {
		b.WriteString(wrapList("Only in `calendar event` (refused here): ", eventOnlyKeys))
	}
	_, _ = io.WriteString(w, b.String())
}

// wrapList prints lead and the keys, comma separated, as lines of at most helpWidth columns
// indented by two spaces.
func wrapList(lead string, keys []string) string {
	const indent = "  "
	var b strings.Builder
	line := indent + lead
	for i, k := range keys {
		word := k
		if i < len(keys)-1 {
			word += ","
		}
		switch {
		case strings.HasSuffix(line, " "): // the first key of the list
			line += word
		case len(line)+1+len(word) > helpWidth:
			b.WriteString(line + "\n")
			line = indent + "  " + word
		default:
			line += " " + word
		}
	}
	b.WriteString(line + "\n")
	return b.String()
}
