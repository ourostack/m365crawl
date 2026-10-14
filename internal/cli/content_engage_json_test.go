package cli

import (
	"encoding/json"
	"github.com/ourostack/m365crawl/internal/engagecontent"
	"strings"
	"testing"
)

func TestEngagePublicProjectionsUseSnakeCase(t *testing.T) {
	fetch := EngageFetchReport{SessionLosses: []engagecontent.Loss{{Code: "clock_unmapped", Count: 1}}}
	b, err := json.Marshal(fetch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"code":"clock_unmapped"`) || !strings.Contains(string(b), `"count":1`) {
		t.Fatal("losses not snake_case:", string(b))
	}
	signin := EngageSigninReport{SourceAccount: engagecontent.SourceAccount{Host: "engage.cloud.microsoft", NetworkID: "network", UserID: "A"}}
	b, err = json.Marshal(signin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"network_id":"network"`) || strings.Contains(string(b), `"NetworkID"`) {
		t.Fatal("account not snake_case:", string(b))
	}
}
