package engagecontent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestEngageEncodedEnvelopeMetadataAndEmptyBlocks(t *testing.T) {
	r := Result{State: "observations", Qualification: "native_home_observations", Account: SourceAccount{"engage.cloud.microsoft", "network", "viewer"}, Losses: []Loss{{"clock_unmapped", 32}}}
	value := strings.Repeat("\x01", 262144)
	for i := 0; i < 8; i++ {
		sender := value
		if i == 7 {
			sender = strings.Repeat("\x01", 262053)
		}
		r.Threads = append(r.Threads, ThreadObservation{Ordinal: i, ID: value, NetworkID: "network", GroupID: value, StarterID: value, SenderID: &sender, Blocks: make([]string, 4096)})
	}
	if err := ValidateResult(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 6*8388608 || len(b) > 51576832 || len(b) >= MaxResultJSONBytes {
		t.Fatalf("escaped final envelope outside proven bound: %d", len(b))
	}
}
