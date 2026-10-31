package cli

import (
	"bytes"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/output"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestWorkIQReportGoldens(t *testing.T) {
	e := textEnv(t)
	total, canonical := int64(4), 2
	report := workIQResult{Status: "ok_with_omissions", WorkIQReport: syncer.WorkIQReport{
		Kind: "coverage", Enabled: true,
		Accounts: []syncer.WorkIQAccountReport{{Family: "mail", Account: "outlook/Synthetic", Status: "ok_with_omissions",
			Counts: map[string]int{"folders": 2}, Gaps: map[string]int{"workiq_folder_count_unknown": 1}}},
		Counts: map[string]int{"folders": 2}, Gaps: map[string]int{"workiq_folder_count_unknown": 1},
		Mail: []syncer.WorkIQMailCoverage{
			{Account: "outlook/Synthetic", FolderID: "folder-one", Basis: "folder_total", ServerTotal: &total, ArchiveCount: &canonical},
			{Account: "outlook/Synthetic", FolderID: "folder-unknown", Basis: "folder_total"},
		},
		Chats: []syncer.WorkIQChatCoverage{{Account: "synthetic-tenant/synthetic-user", Conversation: "chat",
			Since: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			ServerBasis: "all_message_types", ArchiveBasis: "comparable_message_types", ServerCount: 3, ArchiveCount: 1, Complete: true}},
		Probes: []syncer.WorkIQPathProbe{{Family: "mail", Account: "outlook/Synthetic", PathClass: "mail_folders",
			State: "allowed", Scope: "initial_page_only"}},
	}}
	for _, tc := range []struct {
		name   string
		format output.Format
		color  bool
	}{{"plain", output.Text, false}, {"color", output.Text, true}, {"json", output.JSON, false}, {"log", output.Log, false}} {
		var out bytes.Buffer
		rt := newRuntime(t.Context(), &Globals{}, &out, &bytes.Buffer{})
		rt.format, rt.cmd, rt.color = tc.format, "workiq coverage", tc.color
		if err := rt.write(rt.cmd, report); err != nil {
			t.Fatal(err)
		}
		checkGolden(t, "workiq_coverage."+tc.name, e.scrub(out.String()))
	}
}
