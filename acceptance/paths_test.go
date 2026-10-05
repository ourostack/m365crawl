package acceptance

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/v8"
	_ "modernc.org/sqlite"
)

func TestFirstDifference(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{`{"properties":{"mentions":[1,2]}}`, `{"properties":{"mentions":[1,3]}}`, "$.properties.mentions[] [value]"},
		{`{"id":1}`, `{"type":1}`, "$.id [key]"},
		{`{"id":[1]}`, `{"id":{"x":1}}`, "$.id [shape]"},
		{`{"id":"1"}`, `{"id":1}`, "$.id [value]"},
		{`{"1234567":{"x":1}}`, `{"1234567":{"x":2}}`, "$.<field>.<field> [value]"},
		{`[1,2]`, `[1]`, "$[] [shape]"},
	}
	for _, c := range cases {
		if got := firstDifference(c.a, c.b); got != c.want {
			t.Errorf("firstDifference(%s, %s) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// TestDiffScriptAgreesWithGo feeds synthetic payloads through diff.mjs: a decodable value, a
// version 16 relabel, and a payload Node rejects.
func TestDiffScriptAgreesWithGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	payloads := [][]byte{
		{0xff, 0x0f, 0x22, 0x02, 'h', 'i'},
		{0xff, 0x10, 0x22, 0x02, 'h', 'i'},
		{0xff, 0x0f, 0x6f, 0x22, 0x01, 'a', 0x49, 0x02, 0x7b, 0x01}, // {a: 1}
		{0xff, 0x0f, 0x5c},                                          // host object: Node rejects it
	}
	var in bytes.Buffer
	for _, p := range payloads {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(p))) //nolint:gosec // tiny test payloads
		in.Write(n[:])
		in.Write(p)
	}
	cmd := exec.Command(node, "diff.mjs") //nolint:gosec // node comes from PATH; the script is this package's own
	cmd.Dir = diffScriptDir(t)
	cmd.Stdin = &in
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("%v: %s", err, exitErr.Stderr)
		}
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(payloads) {
		t.Fatalf("got %d lines for %d payloads: %q", len(lines), len(payloads), lines)
	}
	for i, p := range payloads[:3] {
		v, err := v8.Deserialize(p)
		if err != nil {
			t.Fatalf("payload %d: %v", i, err)
		}
		c, _ := v8.Canonical(v)
		if want := "ok " + string(c); lines[i] != want {
			t.Errorf("payload %d: node %q, go %q", i, lines[i], want)
		}
	}
	if !strings.HasPrefix(lines[3], "nc ") {
		t.Errorf("rejected payload: %q", lines[3])
	}
}

func TestSafeKey(t *testing.T) {
	for k, want := range map[string]string{
		"messageMap": "messageMap", "$date": "$date", "version": "version",
		"19:abc@thread.v2": "<field>", "8:orgid:1234": "<field>", "Jane Doe": "<field>", "": "<field>",
	} {
		if got := safeKey(k); got != want {
			t.Errorf("safeKey(%q) = %q, want %q", k, got, want)
		}
	}
}

func TestCredentialLeakCount(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"redacted keyed value", `{"access_token":"[redacted]"}`, 0},
		{"star-redacted keyed value", `{"authorization":"******"}`, 0},
		{"prefixed redacted authorization", `{"authorization":"Bearer [redacted]"}`, 0},
		{"authorization header redacted", `{"authorization":"Authorization: [redacted]"}`, 0},
		{"redacted client secret", `{"client_secret":"[redacted]"}`, 0},
		{"null refresh token", `{"refresh_token":null}`, 0},
		{"empty password", `{"password":""}`, 0},
		{"unredacted keyed value", `{"access_token":"abc"}`, 1},
		{"unredacted password", `{"password":"abc"}`, 1},
		{"bearer token string", `{"note":"Bearer abcdefghijklmnop.qrstuvwxyzABCDEF.1234567890abcdef"}`, 1},
		{"opaque bearer token string", `{"note":"Bearer +/=_~abcdefghijklmnop"}`, 1},
		{"redacted bearer string", `{"note":"Authorization: Bearer [redacted]"}`, 0},
		{"bearer prose only", `{"note":"supports Bearer token auth"}`, 0},
		{"field name in prose only", `{"note":"this mentions access_token but not a secret"}`, 0},
		{"stringified redacted json", `{"wrapped":"{\"refresh_token\":\"[redacted]\"}"}`, 0},
		{"stringified unredacted json", `{"wrapped":"{\"refresh_token\":\"abc\"}"}`, 1},
	}
	for _, c := range cases {
		if got := credentialLeakCount(c.in); got != c.want {
			t.Errorf("%s: credentialLeakCount(%s) = %d, want %d", c.name, c.in, got, c.want)
		}
	}
}

func TestCredentialLeakCandidateSQLMatchesSupportedFamilies(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`create table records (source text, database text, store text, value_json text)`); err != nil {
		t.Fatal(err)
	}
	rows := []string{
		`{"token":"eyJabcdefgh.eyJijklmnop.qrstuvwxyz012345"}`,
		`{"note":"Bearer abcdefghijklmnop.qrstuvwxyzABCDEF.1234567890abcdef"}`,
		`{"access_token":"abc"}`,
		`{"refresh_token":"abc"}`,
		`{"id_token":"abc"}`,
		`{"authorization":"abc"}`,
		`{"client_secret":"abc"}`,
		`{"password":"abc"}`,
		`{"note":"safe"}`,
	}
	want := map[string]bool{}
	for i, row := range rows {
		if row != `{"note":"safe"}` {
			want[row] = true
		}
		if _, err := db.Exec(`insert into records(source, database, store, value_json) values (?, ?, ?, ?)`, "src", "db", "store", row); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
	got := map[string]bool{}
	rs, err := db.Query(credentialLeakCandidateSQL())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		var source, database, store, valueJSON string
		if err := rs.Scan(&source, &database, &store, &valueJSON); err != nil {
			t.Fatal(err)
		}
		got[valueJSON] = true
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("credentialLeakCandidateSQL() matched %d rows, want %d", len(got), len(want))
	}
	for row := range want {
		if !got[row] {
			t.Fatalf("credentialLeakCandidateSQL() did not match %s", row)
		}
	}
}

func TestStderrNoteHidesContent(t *testing.T) {
	stderr := "KeyError: 'private-value'"
	note := stderrNote(t, "tool", []byte(stderr))
	if strings.Contains(note, "private-value") || !strings.Contains(note, "25 bytes") {
		t.Errorf("note leaks or miscounts: %q", note)
	}
}
