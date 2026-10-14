package cli

import (
	"encoding/json"
	"github.com/ourostack/m365crawl/internal/store"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEngagePreviewAndClockContract(t *testing.T) {
	r := engageCLIResult("A")
	title := strings.Repeat("title", 100)
	r.Threads[0].Title = &title
	raw := "0001-01-01T00:00:00Z"
	zero := time.Time{}
	version := int64(0)
	flag := false
	r.Threads[0].StarterUpdatedRaw = &raw
	r.Threads[0].StarterUpdatedAt = &zero
	r.Threads[0].Version = &version
	r.Threads[0].IsDraft = &flag
	r.Threads[0].ID = "9007199254740993"
	row := store.EngageObservationRow{ID: 9007199254740993, SourceID: 1, SessionID: 2, Thread: r.Threads[0], SessionLosses: r.Losses}
	row.Thread.Blocks = make([]string, 101)
	row.Thread.Blocks[0] = strings.Repeat("body", 100)
	row.Thread.Blocks[1] = ""
	row.Thread.Blocks[2] = row.Thread.Blocks[0]
	before, _ := json.Marshal(row)
	item := shapeEngage(row, 10)
	if item.ID != "engage-observation:9007199254740993" || item.NativeThreadID != "9007199254740993" || len(item.Blocks) != 100 || item.BlocksTotal != 101 || !item.PreviewTruncated || !item.TextTruncated || item.Blocks[1] != "" || item.Version == nil || *item.Version != 0 || item.IsDraft == nil || *item.IsDraft || item.StarterUpdatedAt == nil || !item.StarterUpdatedAt.IsZero() {
		t.Fatalf("%+v", item)
	}
	item.Blocks[0] = "mutated"
	*item.Title = "mutated"
	*item.StarterUpdatedRaw = "mutated"
	*item.StarterUpdatedAt = time.Now()
	*item.Version = 99
	*item.IsDraft = true
	item.SessionLosses[0].Count = 99
	after, _ := json.Marshal(row)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("shaping aliased original session")
	}
	if item.FullTextSQL != "SELECT o.*,s.* FROM engage_occurrences o JOIN engage_sessions s ON s.rowid=o.session_id WHERE o.rowid=9007199254740993" {
		t.Fatal(item.FullTextSQL)
	}
	row.Thread.Blocks = row.Thread.Blocks[:100]
	item = shapeEngage(row, 0)
	if item.PreviewTruncated || len(item.Blocks) != 100 {
		t.Fatal("100 block boundary")
	}
}
