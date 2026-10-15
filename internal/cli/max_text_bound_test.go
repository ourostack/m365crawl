package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestMaxTextInclusiveRuneBound(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		max, runes        int
		cut               bool
	}{
		{"off", "abcdefghijk", "abcdefghijk", 0, 11, false},
		{"negative-helper-guard", "abc", "abc", -1, 3, false},
		{"empty", "", "", 8, 0, false},
		{"short", "abc", "abc", 8, 3, false},
		{"exact", "abcdefgh", "abcdefgh", 8, 8, false},
		{"one-exact", "é", "é", 1, 1, false},
		{"one-cut", "éx", "…", 1, 1, true},
		{"ascii-eight", "abcdefghijk", "abcdefg…", 8, 8, true},
		{"non-ascii", "héllo wörld", "héll…", 5, 5, true},
		{"non-bmp", "😀😁😂😃😄😅😆😇😈", "😀😁😂😃😄😅😆…", 8, 8, true},
		{"combining", "e\u0301xyz", "e\u0301…", 3, 3, true},
		{"zwj", "👨‍👩‍👧x", "👨‍…", 3, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, cut := truncateRunes(tc.input, tc.max)
			if got != tc.want || cut != tc.cut || utf8.RuneCountInString(got) != tc.runes {
				t.Errorf("decoded rune bound: got=%q runes=%d cut=%t want=%q runes=%d cut=%t",
					got, utf8.RuneCountInString(got), cut, tc.want, tc.runes, tc.cut)
			}
			if tc.max > 0 && utf8.RuneCountInString(got) > tc.max {
				t.Errorf("marker exceeded inclusive%d bound", tc.max)
			}
			if tc.max > 0 {
				again, secondCut := truncateRunes(got, tc.max)
				if again != got || secondCut {
					t.Errorf("reclipping changed bounded value or reported another cut: %q %t", again, secondCut)
				}
			}
		})
	}
}

func TestMaxTextHelpDescribesInclusiveSupportedFields(t *testing.T) {
	e := cloudCLIEnv(t)
	for _, args := range [][]string{{"--help"}, {"content", "show", "--help"}} {
		code, out, stderr := e.run(args...)
		if code != 0 {
			t.Fatal(stderr)
		}
		help := strings.Join(strings.Fields(out), " ")
		for _, fragment := range []string{"Supported commands", "each applicable text/preview field", "Unicode code points", "including any … marker", "0 keeps all text"} {
			if !strings.Contains(help, fragment) {
				t.Errorf("%v help omits %q", args, fragment)
			}
		}
		if strings.Contains(help, "List commands only: truncate") {
			t.Fatal("global max-text help incorrectly excludes detail reads")
		}
	}
}

func TestMaxTextEngagePreviewKeepsEvidenceAndOccurrences(t *testing.T) {
	for _, count := range []int{100, 101} {
		r := engageCLIResult("synthetic-bounded")
		title := "😀😁😂😃😄😅😆😇😈"
		r.Threads[0].Title = &title
		r.Threads[0].ID = "9007199254740993"
		r.Threads[0].Blocks = make([]string, count)
		for i := range r.Threads[0].Blocks {
			r.Threads[0].Blocks[i] = "abcdefghijk"
		}

		r.Threads[0].Blocks[1] = ""
		r.Threads[0].Blocks[3] = "exactly8"
		row := store.EngageObservationRow{ID: 9007199254740993, SourceID: 7, SessionID: 9,
			RepresentationDigest: strings.Repeat("a", 64), Thread: r.Threads[0], SessionLosses: r.Losses}
		before, _ := json.Marshal(row)
		item := shapeEngage(row, 8)
		if len(item.Blocks) != 100 || item.BlocksTotal != count || item.PreviewTruncated != (count == 101) ||
			!item.TextTruncated || item.NativeThreadID != "9007199254740993" || item.ID != "engage-observation:9007199254740993" {
			t.Fatalf("occurrence/ID flags changed: %+v", item)
		}
		if item.Title == nil || *item.Title != "😀😁😂😃😄😅😆…" || utf8.RuneCountInString(*item.Title) != 8 {
			t.Errorf("title exceeds inclusive8: %v", item.Title)
		}
		if item.Blocks[0] != "abcdefg…" || item.Blocks[1] != "" || item.Blocks[2] != item.Blocks[0] || item.Blocks[3] != "exactly8" {
			t.Errorf("ordered empty/duplicate/exact blocks changed: %q", item.Blocks[:4])
		}
		for i, block := range item.Blocks {
			if utf8.RuneCountInString(block) > 8 {
				t.Errorf("block%d exceeds inclusive8: %q", i, block)
			}
		}
		after, _ := json.Marshal(row)
		if !reflect.DeepEqual(before, after) || item.RepresentationDigest != row.RepresentationDigest ||
			!reflect.DeepEqual(item.SessionLosses, engageDisplayLosses(row.SessionLosses)) {
			t.Fatal("display shaping changed immutable evidence")
		}
		item.Blocks[0] = "changed"
		*item.Title = "changed"
		item.SessionLosses[0].Count++
		after, _ = json.Marshal(row)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("clipped display aliases original evidence")
		}
	}
}
