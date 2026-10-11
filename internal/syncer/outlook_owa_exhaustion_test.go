package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestSyncOWAExhaustionPreservesCodeRollbackAndRetry(t *testing.T) {
	isolateTmp(t)
	db, root := newDB(t), owaRoot(t, "mail")
	st, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal("synthetic_archive_open_failed")
	}
	if st.Close() != nil {
		t.Fatal("synthetic_archive_close_failed")
	}
	raw := openRaw(t, db)
	if _, err := raw.Exec(`insert into mail_source_ids(account,kind,source_id,key) values('outlook/Default','message','SyntheticOld',4294967295)`); err != nil {
		t.Fatal("synthetic_exhaustion_seed_failed")
	}
	opts := outlookOpts(db, root)
	opts.Root = t.TempDir()
	rep, _, err := Run(context.Background(), opts)
	src := sourceKeyed(t, rep, "outlook|Default|mail")
	if err == nil || src.Error == nil || src.Status == StatusOK || src.Counts != nil {
		t.Fatal("exhaustion_not_failed")
	}
	if count(t, db, `select count(*) from mail_messages`) != 0 || count(t, db, `select count(*) from mail_source_ids`) != 1 || count(t, db, `select count(*) from mail_source_ids where source_id='SyntheticOld' and key=4294967295`) != 1 {
		t.Fatal("exhaustion_transaction_not_rolled_back")
	}
	for _, table := range []string{"mail_folders", "mail_recipients", "mail_attachments", "mail_coverage", "mail_absent", "mail_fts"} {
		if count(t, db, "select count(*) from "+table) != 0 {
			t.Fatal("exhaustion_partial_mail_state_committed")
		}
	}
	if count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Default'`) != 0 || count(t, db, `select count(*) from sync_runs where source='outlook|Default|mail' and status in ('ok','unchanged')`) != 0 {
		t.Fatal("exhaustion_vouched_for_successful_read")
	}
	if src.Error.Code != "outlook_mail_key_exhausted" || src.Error.Fix != "Keep the existing archive for reads; use a separate private archive for further syncs." {
		t.Error("exhaustion_code_or_remedy_lost")
	}
	var data string
	if raw.QueryRow(`select value from meta where key='outlook_mail_failure:outlook/Default'`).Scan(&data) != nil {
		t.Fatal("exhaustion_failure_receipt_missing")
	}
	var remembered struct {
		Code string `json:"code"`
		Fix  string `json:"fix"`
	}
	if json.Unmarshal([]byte(data), &remembered) != nil || remembered.Code != "outlook_mail_key_exhausted" || remembered.Fix != src.Error.Fix {
		t.Error("persisted_exhaustion_code_or_remedy_lost")
	}
	// Repair only the synthetic high-water seed; normal next Run must not skip the failed source.
	if _, err := raw.Exec(`delete from mail_source_ids where source_id='SyntheticOld'`); err != nil {
		t.Fatal("synthetic_exhaustion_repair_failed")
	}
	rep, _, err = Run(context.Background(), opts)
	src = sourceKeyed(t, rep, "outlook|Default|mail")
	if err != nil || src.Status != StatusOK || src.Error != nil || count(t, db, `select count(*) from mail_messages`) != 5 || count(t, db, `select count(*) from meta where key='outlook_mail_failure:outlook/Default'`) != 0 {
		t.Fatal("exhaustion_blocked_next_eligible_read")
	}
}

func TestOWAExhaustionSanitizerUsesOnlyFixedDetails(t *testing.T) {
	private := "private-source-path-never-output"
	input := &errs.Coded{Code: "outlook_mail_key_exhausted", Exit: errs.ExitEnvironment, Message: private, Fix: private}
	got := owaSourceError(input)
	var coded *errs.Coded
	if !errors.As(got, &coded) || coded.Code != "outlook_mail_key_exhausted" || coded.Message != "the archive has no numeric mail identity keys left for this account and kind" || coded.Fix != "Keep the existing archive for reads; use a separate private archive for further syncs." || strings.Contains(got.Error(), private) {
		t.Fatal("exhaustion_boundary_not_fixed_safe")
	}
}
