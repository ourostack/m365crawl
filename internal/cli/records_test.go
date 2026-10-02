package cli

import (
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/errs"
)

func TestStoresListsEveryArchivedStore(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "stores")
	m := decode(t, stdout)
	rows := items(t, m)
	if len(rows) != 4 || m["truncated"] != false {
		t.Fatalf("stores = %v", m)
	}
	var live float64
	for i, r := range rows {
		for _, k := range []string{"database", "store", "records", "removed", "last_updated_at"} {
			if _, ok := r[k]; !ok {
				t.Errorf("stores row lacks %q: %v", k, r)
			}
		}
		if i > 0 && rows[i-1]["database"].(string) > r["database"].(string) {
			t.Errorf("not sorted by database: %v", rows)
		}
		live += r["records"].(float64)
	}
	if live != 12 {
		t.Fatalf("records across stores = %v, want 12", live)
	}
	_, stdout, _ = e.run("--max-age", "0", "--account", tenantA+"/"+userA, "--fields", "store,records", "stores")
	rows = items(t, decode(t, stdout))
	if len(rows) != 2 || len(rows[0]) != 2 || rows[0]["store"] == nil {
		t.Fatalf("account and fields: %v", rows)
	}
	code, _, stderr := e.run("--max-age", "0", "--fields", "nope", "stores")
	if code != errs.ExitUsage || !strings.Contains(errorOf(t, stderr)["message"].(string), "unknown --fields key") {
		t.Fatalf("bad field: %d %s", code, stderr)
	}
}

func TestRecordsJSONValueParsed(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "records", "--database", "Teams:calendar-manager", "--store", "events")
	m := decode(t, stdout)
	rows := items(t, m)
	if len(rows) != 4 {
		t.Fatalf("records = %v", m)
	}
	for _, r := range rows {
		value, ok := r["value_json"].(map[string]any)
		if !ok {
			t.Fatalf("value_json is %T, want a parsed object: %v", r["value_json"], r)
		}
		if value["id"] == nil || r["key_json"] != value["id"] {
			t.Errorf("key_json %v, value %v", r["key_json"], value)
		}
		if r["store"] != "events" || !strings.HasPrefix(r["database"].(string), "Teams:calendar-manager:") || r["tenant_id"] == nil || r["updated_at"] == nil || r["first_seen_at"] == nil {
			t.Errorf("record = %v", r)
		}
		if _, ok := r["removed_at"]; ok {
			t.Errorf("a live record has no removed_at: %v", r)
		}
	}
	// Arrays and binary keys come back as parsed JSON too.
	_, stdout, _ = e.run("--max-age", "0", "records", "--database", "Teams:pinned-manager", "--limit", "8")
	kinds := map[string]bool{}
	for _, r := range items(t, decode(t, stdout)) {
		switch r["key_json"].(type) {
		case string:
			kinds["string"] = true
		case []any:
			kinds["array"] = true
		case map[string]any:
			kinds["bytes"] = true
		}
	}
	if !kinds["string"] || !kinds["array"] || !kinds["bytes"] {
		t.Fatalf("key kinds = %v", kinds)
	}
}

func TestRecordsLimitMaxTextFieldsAndFilters(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "records", "--database", "Teams:pinned", "--limit", "3", "--max-text", "10")
	m := decode(t, stdout)
	rows := items(t, m)
	if len(rows) != 3 || m["truncated"] != true || m["total"] != float64(8) {
		t.Fatalf("limit: %v", m)
	}
	for _, r := range rows {
		v, ok := r["value_json"].(string)
		if !ok || r["text_truncated"] != true || !strings.HasSuffix(v, "…") {
			t.Fatalf("max-text: %v", r)
		}
	}
	_, stdout, _ = e.run("--max-age", "0", "--fields", "key_json,value_json", "--max-text", "10", "records", "--database", "Teams:pinned", "--limit", "1")
	r := items(t, decode(t, stdout))[0]
	if len(r) != 3 || r["text_truncated"] != true {
		t.Fatalf("fields keep text_truncated beside value_json: %v", r)
	}
	_, stdout, _ = e.run("--max-age", "0", "records", "--database", "Teams:pinned", "--since", "1h")
	if n := len(items(t, decode(t, stdout))); n != 8 {
		t.Fatalf("--since 1h keeps the fresh sync's rows: %d", n)
	}
	_, stdout, _ = e.run("--max-age", "0", "records", "--database", "Teams:pinned", "--since", "2999-01-01")
	if n := len(items(t, decode(t, stdout))); n != 0 {
		t.Fatalf("--since in the future: %d", n)
	}
	_, stdout, _ = e.run("--max-age", "0", "--account", tenantA+"/"+userA, "records", "--database", "Teams:")
	if n := len(items(t, decode(t, stdout))); n != 6 {
		t.Fatalf("one account's records: %d", n)
	}
	// A removed record is hidden until --include-removed.
	e.exec(`update records set removed_at = updated_at where store = 'pins' and key_json = '"fixture-pin-1"'`)
	_, stdout, _ = e.run("--max-age", "0", "records", "--database", "Teams:pinned", "--include-removed")
	removed := 0
	for _, it := range items(t, decode(t, stdout)) {
		if it["removed_at"] != nil {
			removed++
		}
	}
	if removed != 1 {
		t.Fatalf("include-removed lists %d removed rows, want 1", removed)
	}
	_, stdout, _ = e.run("--max-age", "0", "records", "--database", "Teams:pinned")
	if n := len(items(t, decode(t, stdout))); n != 7 {
		t.Fatalf("removed rows hidden by default: %d", n)
	}
}

func TestRecordsUsageErrors(t *testing.T) {
	e := newEnv(t)
	e.sync()
	for _, args := range [][]string{
		{"records"},
		{"records", "--database", "Teams:", "--limit", "0"},
		{"records", "--database", "Teams:", "--since", "nonsense"},
		{"--fields", "nope", "records", "--database", "Teams:"},
	} {
		code, _, stderr := e.run(append([]string{"--max-age", "0"}, args...)...)
		if code != errs.ExitUsage || errorOf(t, stderr)["code"] != errs.CodeUsage {
			t.Errorf("%v: exit %d, %s", args, code, stderr)
		}
	}
	code, stdout, _ := e.run("--max-age", "0", "records", "--database", "Teams:no-such-manager")
	if m := decode(t, stdout); code != 0 || len(items(t, m)) != 0 {
		t.Fatalf("an unknown database is an empty list: %d %v", code, m)
	}
}

func TestRecordsTextIsOneLinePerRecord(t *testing.T) {
	e := textEnv(t)
	e.sync()
	e.exec(`update records set removed_at = updated_at where key_json = '"fixture-event-1-1"'`)
	_, out, _ := e.run("--format", "text", "--max-age", "0", "records", "--database", "Teams:calendar", "--include-removed")
	if strings.Count(out, "(removed)") != 1 || !strings.Contains(out, `"fixture-event-1-2"`) {
		t.Fatalf("text:\n%s", out)
	}
	_, out, _ = e.run("--format", "text", "--max-age", "0", "--fields", "store,key_json", "records", "--database", "Teams:calendar")
	if !strings.Contains(out, "events") {
		t.Fatalf("projected text:\n%s", out)
	}
}

func TestStoresAndRecordsReportArchiveErrors(t *testing.T) {
	e := newEnv(t)
	e.sync()
	e.exec(`drop table records`)
	for _, args := range [][]string{{"stores"}, {"records", "--database", "Teams:"}} {
		code, _, stderr := e.run(append([]string{"--max-age", "0"}, args...)...)
		if code != errs.ExitRuntime || errorOf(t, stderr)["code"] != errs.CodeDBError {
			t.Errorf("%v: exit %d, %s", args, code, stderr)
		}
	}
}
