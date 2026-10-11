package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestDocumentsNativeJSONMissingTimesAreNullNotYearOneClaims(t *testing.T) {
	at := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	model := officedocuments.Document{
		Modified: officedocuments.Timestamp{Raw: "2026-10-11T01:00:00Z", Value: at},
		Sharing:  &officedocuments.Sharing{At: officedocuments.Timestamp{Raw: "unrecognized native value"}},
		Activity: &officedocuments.Activity{},
	}
	raw, err := json.Marshal(documentItemOf(store.DocumentObservationRow{ID: 1, Document: model}, 0))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Native struct {
			Opened, Created, Modified struct {
				Raw   string
				Value *time.Time
			}
			Sharing struct {
				At struct {
					Raw   string
					Value *time.Time
				}
			}
			Activity struct{ At struct{ Value *time.Time } }
		} `json:"native"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Native.Opened.Value != nil || result.Native.Created.Value != nil ||
		result.Native.Sharing.At.Value != nil || result.Native.Activity.At.Value != nil ||
		result.Native.Sharing.At.Raw != "unrecognized native value" ||
		result.Native.Modified.Value == nil || !result.Native.Modified.Value.Equal(at) ||
		strings.Contains(string(raw), "0001-01-01") {
		t.Fatal("source-model sentinel became a native event clock or valid/raw source clock lost")
	}
}
