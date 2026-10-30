package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

// Live: doctor said "the last sync failed" and pointed at `m365crawl sync` after a failed WorkIQ run,
// although the last desktop sync was fine.
func TestDoctorLastSyncIgnoresWorkIQRuns(t *testing.T) {
	e := newEnv(t)
	e.sync()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := json.Marshal([]string{"mail", mailAccount})
	if err := st.RecordRun(context.Background(), store.Run{Source: "workiq:" + string(key), Status: "failed", ErrorCode: "workiq_failed",
		StartedAt: mailT0, FinishedAt: mailT0, SourceAccounts: []string{mailAccount}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, out, _ := e.run("--json", "doctor")
	for _, raw := range decode(t, out)["checks"].([]any) {
		c := raw.(map[string]any)
		if c["name"] == "last_sync_status" && c["warn"] == true {
			t.Fatal("doctor blamed the desktop sync for a WorkIQ run", c)
		}
	}
}
