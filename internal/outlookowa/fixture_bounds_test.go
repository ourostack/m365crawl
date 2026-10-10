package outlookowa

import (
	"testing"

	"github.com/ourostack/m365crawl/internal/indexeddb"
)

func TestFaultReaderRejectsUnrepresentableSyntheticKeys(t *testing.T) {
	for _, store := range []int64{-1, 256} {
		f := &faultReader{values: map[int64][]any{store: {"synthetic"}}}
		called := false
		if err := f.Records(1, store, func(indexeddb.Record) error { called = true; return nil }); err == nil || called {
			t.Fatalf("unrepresentable store %d accepted or callback invoked: %v", store, err)
		}
	}
	f := &faultReader{values: map[int64][]any{1: make([]any, 257)}}
	if err := f.Records(1, 1, func(indexeddb.Record) error { return nil }); err == nil {
		t.Fatal("index 256 silently wrapped to zero")
	}
}

func TestFaultReaderRejectsMalformedSyntheticRecord(t *testing.T) {
	f := readerWith("synthetic")
	for _, raw := range [][]byte{nil, {2}, {2, 9}, {9, 0}} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("malformed record panicked instead of error: %v", r)
				}
			}()
			if _, err := f.Decode(1, raw); err == nil {
				t.Errorf("malformed record accepted: %v", raw)
			}
		}()
	}
}

func TestObjectFixtureRejectsOddKeyValuePairs(t *testing.T) {
	defer func() {
		if got := recover(); got != "synthetic object requires key/value pairs" {
			t.Errorf("odd fixture arguments must have fixed diagnostic, got %v", got)
		}
	}()
	obj("unpaired") //nolint:staticcheck // SA5012: deliberately malformed fixture input proves the fixed failure diagnostic.
}
