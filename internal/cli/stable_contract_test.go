package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

// TestStableV1DocumentationContract catches contradictory normative promises:
// a changelog-only break, reversed archive direction, list-only clipping, or
// optional Darwin signing. It reads repository docs, never an installed archive.
func TestStableV1DocumentationContract(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "..", path)) //nolint:gosec // exact checked-in documentation
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	spec, agents := read("SPEC.md"), read("AGENTS.md")
	section := func(t *testing.T, doc, heading string) string {
		t.Helper()
		start := strings.Index(doc, heading+"\n")
		if start < 0 {
			t.Fatalf("missing section %q", heading)
		}
		body := doc[start+len(heading)+1:]
		level := strings.IndexByte(heading, ' ')
		for i, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "#") {
				n := strings.IndexByte(line, ' ')
				if n > 0 && n <= level {
					return strings.Join(strings.Split(body, "\n")[:i], "\n")
				}
			}
		}
		return body
	}
	check := func(t *testing.T, label, body string, required, forbidden []string) {
		t.Helper()
		// Ignore wrapping, but not section boundaries or semantic contradictions.
		body = strings.Join(strings.Fields(body), " ")
		for _, pattern := range required {
			if !regexp.MustCompile("(?i)" + pattern).MatchString(body) {
				t.Errorf("%s: missing contract clause /%s/", label, pattern)
			}
		}
		for _, pattern := range forbidden {
			if regexp.MustCompile("(?i)" + pattern).MatchString(body) {
				t.Errorf("%s: contradictory contract clause /%s/", label, pattern)
			}
		}
	}
	version := section(t, spec, "## 10. Versioning and compatibility")
	t.Run("major_version_rule", func(t *testing.T) {
		check(t, "SPEC 10", version, []string{
			`1\.x`, `incompatible public.*require a new major`,
			`changelog.*(alone|not permission)`,
			`additive fields`, `open-vocabulary`,
			`meaning.*types.*nullability.*default acquisition`,
		}, []string{`stable within 0\.x.*except`, `except where .*changelog.*break`})
		for _, tc := range []struct{ label, body string }{
			{"SPEC SQL", section(t, spec, "### 3.1 Tables")},
			{"SPEC machine keys", section(t, spec, "### 6.2 Streams and documents")},
			{"AGENTS archive policy", section(t, agents, "## Archive rules")},
		} {
			check(t, tc.label, tc.body, []string{`new major`, `1\.x`},
				[]string{`breaking change recorded in`, `changes go through the changelog.*adding a field is compatible`})
		}
	})
	t.Run("public_surface_exceptions", func(t *testing.T) {
		check(t, "SPEC 10 public table", version, []string{
			`supported commands.*flags.*defaults`,
			`field names.*types.*null.*omission`,
			`text.*TTY.*JSON.*otherwise`, `log.*label`,
			`max-age.*15m.*archive-only`,
			`stderr.*1/2/3/4.*130`, `degraded.*warnings`,
			`items.*count.*truncated.*total.*sync.*notices`,
			`archive_age_seconds.*null`,
			`watch.*JSON Lines.*in-band`,
			`skill.*Markdown`, `metadata.*fixed.*JSON`,
			`native.*null.*omission`,
			`records.*decoded.JSON.*clipped.string`,
			`published SQL columns`, `source.*account.*native`,
			`archive-scoped.*observation`, `draft.*revision`,
			`human prose.*key order.*whitespace.*internal columns`,
			`cache.*(universal|universality)`,
		}, nil)
		for _, tc := range []struct {
			label, body, row    string
			required, forbidden []string
		}{
			{"SPEC global max-age", section(t, spec, "### 6.1 Global flags and environment"), "| `--max-age DURATION`",
				[]string{`archive-only.*no-acquisition`, `registered-source reads may refresh registered inputs`},
				[]string{`archive-only and registered-source reads.*no-acquisition`}},
			{"SPEC public defaults", version, "| Supported commands, flags and defaults |",
				[]string{
					`content.*drafts.*native-attendee.*archive-only`,
					`registered-source reads.*native people.*notes.*docs.*Loop.*may refresh.*registered inputs`,
					`max-age.*minimum-interval.*qualification`,
					`native people.*max-age 0.*no capture.*discovery.*archive repair`,
					`people reads never implicitly discover unregistered sources`,
				},
				[]string{`archive-only reads.*native people/attendees and registered-source reads.*do not.*acquire`}},
		} {
			row := ""
			for _, line := range strings.Split(tc.body, "\n") {
				if strings.HasPrefix(line, tc.row) {
					row = line
					break
				}
			}
			if row == "" {
				t.Fatalf("%s: missing table row %q", tc.label, tc.row)
			}
			check(t, tc.label, row, tc.required, tc.forbidden)
		}
		check(t, "SPEC 6.5 records", section(t, spec, "### 6.5 Item shapes"),
			[]string{`value_json.*parsed JSON`, `JSON string.*text_truncated`}, nil)
		check(t, "SPEC 7", section(t, spec, "## 7. `watch`"),
			[]string{`JSON Lines`, `error.*watching continues`, `text mode`}, nil)
	})
	t.Run("archive_direction", func(t *testing.T) {
		check(t, "SPEC 10", version, []string{
			fmt.Sprintf(`SQLite schema version \(%d\)`, store.SchemaVersion),
			fmt.Sprintf(`derivation version \(%d`, store.DerivationVersion),
			`reads and writes.*archive_newer`,
			`directional`, `no downgrade`,
			`1\.0.*future 1\.x`,
		}, []string{`refused for writing with`})
		check(t, "SPEC 3.2", section(t, spec, "### 3.2 Archive semantics"),
			[]string{`schema version.*read commands too`, `derivation version.*before any write`}, nil)
		check(t, "SPEC archive_newer error", section(t, spec, "### 6.7 Errors"),
			[]string{`schema.*reads and writes`, `derivation.*before.*writ`},
			[]string{`newer.schema archive.*read.*work`, `read commands still work`})
		check(t, "AGENTS current schema", section(t, agents, "## Archive rules"),
			[]string{fmt.Sprintf(`archive schema is version %d`, store.SchemaVersion)}, nil)
	})
	t.Run("supported_max_text_routes", func(t *testing.T) {
		check(t, "SPEC globals", section(t, spec, "### 6.1 Global flags and environment"),
			[]string{`max-text.*supported.*detail`, `fields.*unsupported.*usage`},
			[]string{`max-text.*List commands only`, `fields.*and.*max-text.*not a list command`})
		check(t, "SPEC clipping", section(t, spec, "### 6.6 `--fields`, `--max-text` and `text_truncated`"),
			[]string{
				`each supported.*field independently`, `Unicode code points`, `N−1.*…`, `N=1`,
				`exact-N.*shorter.*empty.*unchanged`, `0.*keeps all text`,
				`not UTF-8 bytes.*terminal columns.*grapheme`,
				`mail show.*drafts show.*content show`,
				`content show.*default.*0.*unbounded`,
				`drafts list.*300`,
				`command-specific`, `native.ID.*terminal.width.*occurrence`,
			}, nil)
	})
	t.Run("release_signing", func(t *testing.T) {
		check(t, "SPEC 9", section(t, spec, "## 9. Known limits"), []string{
			`Darwin.*MUST.*Developer ID signed.*notarized`,
			`missing.*secrets.*(fail|block)`,
			`Windows.*intentionally unsigned.*verified`,
		}, []string{`notarized only when`, `release notes.*say which`})
		check(t, "SPEC 8", section(t, spec, "## 8. Privacy and security"),
			[]string{`release.*signing.*section 9`}, nil)
	})
}
