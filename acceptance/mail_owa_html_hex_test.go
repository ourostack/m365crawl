//go:build acceptance

package acceptance

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestOWAHTMLMatchesSQLHex(t *testing.T) {
	c := owaObserve(t, owaFixtureProfile("mail", "SyntheticSQLHex"))
	path := filepath.Join(owaScratch(t), "html-hex.db")
	opts := syncer.Options{
		Root: filepath.Join(c.root, "no-teams"), DBPath: path,
		OutlookRoot: c.root, OutlookEnabled: true, OutlookMinReadInterval: -1,
	}
	rep, _, err := syncer.Run(c.ctx, opts)
	owaSyncChecks(t, rep, err, c, 0)
	st := owaArchive(t, path, c.failure)
	_, rows, truncated, err := st.SQL(c.ctx, `select typeof(body_html_gz),body_html_gz from mail_messages where body_text='Synthetic normal cached HTML'`, 2)
	if err != nil || truncated || len(rows) != 1 || len(rows[0]) != 2 || rows[0][0] != "blob" {
		t.Fatal("sql_blob_fixture_unqualified")
	}
	value, ok := rows[0][1].(string)
	if !ok || value == "" {
		t.Fatal("sql_blob_hex_representation_unqualified")
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) < 10 || raw[0] != 0x1f || raw[1] != 0x8b {
		t.Fatal("sql_blob_hex_gzip_unqualified")
	}
	// Independent literal from scripts/fixture/outlook-store.mjs, not the comparator.
	const expected = "<p>Synthetic normal cached HTML</p>"
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal("sql_blob_fixture_gzip_refused")
	}
	body, readErr := io.ReadAll(io.LimitReader(reader, int64(len(expected))+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(body) != expected {
		t.Fatal("sql_blob_fixture_body_unqualified")
	}
	if !owaHTMLMatches(rows[0][1], expected) {
		t.Error("actual_sql_hex_body_refused")
	}

	gzipHex := func(html string) string {
		t.Helper()
		var b bytes.Buffer
		writer := gzip.NewWriter(&b)
		if _, err := io.WriteString(writer, html); err != nil {
			t.Fatal("synthetic_gzip_write_failed")
		}
		if err := writer.Close(); err != nil {
			t.Fatal("synthetic_gzip_close_failed")
		}
		return hex.EncodeToString(b.Bytes())
	}
	badCRC := append([]byte(nil), raw...)
	badCRC[len(badCRC)-8] ^= 0x80
	for _, tc := range []struct {
		name     string
		value    any
		expected string
		want     bool
	}{
		{"null-empty", nil, "", true},
		{"null-nonempty", nil, expected, false},
		{"empty-string-is-not-null", "", "", false},
		{"nonnull-empty-body-is-not-null", gzipHex(""), "", false},
		{"nonnull-body-empty-expected", value, "", false},
		{"odd-hex", "abc", expected, false},
		{"nonhex", "zz", expected, false},
		{"whitespace-in-hex", value + " ", expected, false},
		{"hex-not-gzip", "00010203", expected, false},
		{"raw-gzip-is-not-sql-hex", string(raw), expected, false},
		{"wrong-byte-type", raw, expected, false},
		{"wrong-integer-type", int64(1), expected, false},
		{"wrong-boolean-type", true, expected, false},
		{"different-html", gzipHex("<p>different synthetic HTML</p>"), expected, false},
		{"oversize-decoded-html", gzipHex(expected + strings.Repeat("X", 1<<20)), expected, false},
		{"truncated-gzip", hex.EncodeToString(raw[:len(raw)-2]), expected, false},
		{"bad-gzip-checksum", hex.EncodeToString(badCRC), expected, false},
		{"extra-gzip-member", value + gzipHex("extra"), expected, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := owaHTMLMatches(tc.value, tc.expected); got != tc.want {
				t.Errorf("body_comparator_contract actual=%t expected=%t", got, tc.want)
			}
		})
	}
}
