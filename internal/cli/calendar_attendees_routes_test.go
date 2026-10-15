package cli

import (
	"bytes"
	"strings"
	"testing"
)

func attendeeCommandWords(t *testing.T, command string) []string {
	t.Helper()
	var out []string
	var word strings.Builder
	var quote rune
	started := false
	escaped := false
	for _, c := range command {
		if escaped {
			word.WriteRune(c)
			escaped = false
			started = true
			continue
		}
		if quote == 0 && c == '\\' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			started = true
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			started = true
		case ' ', '\n', '\t':
			if started {
				out = append(out, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(c)
			started = true
		}
	}
	if quote != 0 || escaped {
		t.Fatal("emitted route has unclosed shell quote/escape")
	}
	if started {
		out = append(out, word.String())
	}
	return out
}

func TestAttendeeEmittedRoutesExecuteSelectedArchive(t *testing.T) {
	e := newEnv(t)
	ref := attendeeCLIArchive(t, e, 1, true)
	code, out, stderr := e.run("--max-age", "0", "--max-text", "1", "calendar", "attendees", "list", ref)
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	value := decode(t, out)
	item := items(t, value)[0]
	coverage := value["native_attendee_evidence"].(map[string]any)
	source := coverage["sources"].([]any)[0].(map[string]any)
	for _, command := range []string{item["full_record_command"].(string), source["full_capture_command"].(string)} {
		words := attendeeCommandWords(t, command)
		if len(words) < 7 || words[0] != "m365crawl" {
			t.Fatal("bad emitted executable", words)
		}
		var stdout, stderr bytes.Buffer
		if code := Main(append(words[1:], "--json"), &stdout, &stderr); code != 0 {
			t.Fatal("emitted command failed", code, stdout.String(), stderr.String())
		}
		result := decode(t, stdout.String())
		if len(result["rows"].([]any)) != 1 {
			t.Fatal("full-record route selected another archive or row", result)
		}
	}
}

func TestAttendeePositiveArchiveIDsAndPreview(t *testing.T) {
	e := newEnv(t)
	ref := attendeeCLIArchive(t, e, 101, false)
	for _, id := range []string{"11", "attendee-observation:0", "attendee-observation:-1", "attendee-observation:+1", "attendee-observation:01", "attendee-observation:9223372036854775808", "https://example.invalid/person"} {
		if code, _, _ := e.run("--max-age", "0", "calendar", "attendees", "show", id); code == 0 {
			t.Fatal("noncanonical archive ID accepted", id)
		}
	}
	for _, limit := range []string{"100", "101"} {
		code, out, stderr := e.run("--max-age", "0", "calendar", "attendees", "list", ref, "--limit", limit)
		if code != 0 {
			t.Fatal(code, out, stderr)
		}
		result := decode(t, out)
		rows := items(t, result)
		if len(rows) != 100 || result["truncated"] != true || result["total"] != float64(101) {
			t.Fatal("hard preview/full total lost", result)
		}
		ids := map[string]bool{}
		for _, row := range rows {
			if ids[row["id"].(string)] || row["ambiguous"] != false {
				t.Fatal("identical repeats collapsed or became ambiguous", row)
			}
			ids[row["id"].(string)] = true
		}
	}
	if code, _, _ := e.run("calendar", "attendees", "list", ref, "--limit", "0"); code == 0 {
		t.Fatal("nonpositive preview limit accepted")
	}
}
