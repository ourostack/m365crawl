package cli

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func steeringNotices(t *testing.T, result map[string]any) string {
	t.Helper()
	coverage := result["native_attendee_evidence"].(map[string]any)
	var lines []string
	for _, raw := range coverage["notices"].([]any) {
		lines = append(lines, raw.(string))
	}
	return strings.Join(lines, "\n")
}

func requireOwnerSteering(t *testing.T, text string) {
	t.Helper()
	for _, want := range []string{
		"captured and current linked owner", "profile account is provenance",
		"empty scoped result does not prove", "same selected --db",
		"same event/observation ID", "without --account",
		"changes inspection scope", "not ownership or current membership",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing account-context meaning %q in %s", want, text)
		}
	}
}

func TestInstalledSteeringUnlinkedProfileAndRequestIsolation(t *testing.T) {
	e := newEnv(t)
	ref := attendeeCLIArchive(t, e, 2, false)
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	oldSync := runAttendeeSync
	t.Cleanup(func() { runAttendeeSync = oldSync })
	acquisitions := 0
	runAttendeeSync = func(context.Context, syncer.AttendeeOptions) (syncer.AttendeeReport, error) {
		acquisitions++
		return syncer.AttendeeReport{}, nil
	}
	for _, fields := range []string{"id", "id,scope_state"} {
		args := []string{"--max-age", "1ns", "--fields", fields, "calendar", "attendees", "list", ref, "--limit", "1"}
		code, out, stderr := e.run(args...)
		if code != 0 {
			t.Fatal(code, stderr)
		}
		unscoped := decode(t, out)
		if unscoped["count"] != float64(1) || unscoped["total"] != float64(2) || unscoped["truncated"] != true {
			t.Fatal("source-local physical evidence changed", out)
		}
		if unscoped["native_attendee_evidence"].(map[string]any)["bindings_total"] != "1" {
			t.Fatal("source selection was confused with physical rows", out)
		}
		code, out, stderr = e.run(append(append([]string{}, args...), "--account", "outlook/Main")...)
		if code != 0 {
			t.Fatal(code, stderr)
		}
		scoped := decode(t, out)
		if len(items(t, scoped)) != 0 || scoped["native_attendee_evidence"].(map[string]any)["bindings_total"] != "0" {
			t.Fatal("account guidance widened source-local admission", out)
		}
		requireOwnerSteering(t, steeringNotices(t, scoped))
		sources := scoped["native_attendee_evidence"].(map[string]any)["sources"].([]any)
		if len(sources) != 1 || sources[0].(map[string]any)["account"] != "outlook/Main" {
			t.Fatal("diagnostic source provenance disappeared", out)
		}
		_, again, _ := e.run(args...)
		if strings.Contains(steeringNotices(t, decode(t, again)), "changes inspection scope") {
			t.Fatal("previous request notice leaked into an unscoped response")
		}
	}
	for _, id := range []string{"attendee-observation:1", "attendee-observation:999"} {
		code, _, stderr := e.run("--max-age", "0", "--account", "outlook/Main", "calendar", "attendees", "show", id)
		if code != 2 || errorOf(t, stderr)["code"] != "outlook_attendee_unknown" {
			t.Fatal("scoped unknown changed its code/exit", code, stderr)
		}
		requireOwnerSteering(t, errorOf(t, stderr)["fix"].(string))
	}
	if code, out, stderr := e.run("--max-age", "0", "--fields", "id", "calendar", "attendees", "show", "attendee-observation:1"); code != 0 || len(items(t, decode(t, out))) != 1 {
		t.Fatal("deliberate same-archive/source-local inspection stopped working", code, out, stderr)
	}
	for _, args := range [][]string{
		{"--account", "outlook/Main", "--format", "text", "--fields", "id", "calendar", "attendees", "list", ref, "--limit", "1"},
		{"--account", "outlook/Main", "--fields", "id", "calendar", "attendees", "list", ref, "--source-id", "999", "--limit", "1"},
	} {
		code, out, stderr := e.run(append([]string{"--max-age", "0"}, args...)...)
		if code != 0 {
			t.Fatal(code, stderr)
		}
		requireOwnerSteering(t, out)
	}
	if code, _, _ := e.run("--max-age", "0", "calendar", "attendees", "list", ref, "--limit", "0"); code != 2 {
		t.Fatal("minimum preview was relaxed")
	}
	after, err := os.ReadFile(e.db)
	if err != nil || string(after) != string(before) || acquisitions != 0 {
		t.Fatal("steering read wrote, acquired or retried", err, acquisitions)
	}
}

func TestInstalledSteeringSourcesRemainArchiveWide(t *testing.T) {
	e := newEnv(t)
	attendeeCLIArchive(t, e, 2, false)
	var unscoped []map[string]any
	for _, account := range []string{"", "outlook/Main", "foreign/principal"} {
		args := []string{"--max-age", "0", "--fields", "source_id,account,scope_state", "calendar", "attendees", "sources"}
		if account != "" {
			args = append(args, "--account", account)
		}
		code, out, stderr := e.run(args...)
		if code != 0 {
			t.Fatal(code, stderr)
		}
		result := decode(t, out)
		got := items(t, result)
		if account == "" {
			unscoped = got
		}
		if len(got) != 1 || !reflect.DeepEqual(got, unscoped) {
			t.Fatal("account changed archive-wide source diagnostics", account, out)
		}
		for _, want := range []string{"archive-wide diagnostics", "--account does not owner-filter", "--source-id", "list/sources", "not show"} {
			if !strings.Contains(steeringNotices(t, result), want) {
				t.Errorf("missing source-domain distinction %q", want)
			}
		}
		for _, sourceID := range []string{"1", "999"} {
			code, out, stderr = e.run(append(append([]string{}, args...), "--source-id", sourceID)...)
			if sourceID == "999" {
				if code != 2 || errorOf(t, stderr)["code"] != "outlook_attendee_unknown" {
					t.Fatal("unknown source selector changed", code, out, stderr)
				}
				continue
			}
			if code != 0 || len(items(t, decode(t, out))) != 1 {
				t.Fatal("source-ID narrowing changed", sourceID, code, out, stderr)
			}
		}
	}
}

func TestInstalledSteeringOwnerControlsAndUnknownParity(t *testing.T) {
	e := newEnv(t)
	e.sync()
	owner := tenantA + "/" + userA
	ref := attendeeCLIArchiveWithOwner(t, e, 1, false, owner)
	show := func(account, id string) (int, string, string) {
		return e.run("--max-age", "0", "--account", account, "calendar", "attendees", "show", id)
	}
	if code, out, stderr := show(owner, "attendee-observation:1"); code != 0 || len(items(t, decode(t, out))) != 1 {
		t.Fatal("compatible captured/current linked owner rejected", code, out, stderr)
	}
	code, out, stderr := e.run("--max-age", "0", "--account", owner, "calendar", "attendees", "list", ref, "--history", "--source-id", "1", "--limit", "1")
	if code != 0 || len(items(t, decode(t, out))) != 1 {
		t.Fatal("linked-owner event positive changed", code, out, stderr)
	}
	var previous map[string]any
	for _, id := range []string{"attendee-observation:1", "attendee-observation:999"} {
		code, out, stderr = show("foreign/principal", id)
		if code != 2 || out != "" {
			t.Fatal("foreign/absent detail projected evidence", code, out, stderr)
		}
		got := errorOf(t, stderr)
		if got["code"] != "outlook_attendee_unknown" || !strings.Contains(got["message"].(string), "requested account scope") {
			t.Fatal("unknown lost generic scoped code/meaning", got)
		}
		if previous != nil && !reflect.DeepEqual(got, previous) {
			t.Fatal("unknown error revealed whether the ID exists", previous, got)
		}
		previous = got
	}
	s, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.SetOutlookLink(context.Background(), "outlook/Main", "", at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOutlookLink(context.Background(), "outlook/Main", owner, at); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, history := range []bool{false, true} {
		args := []string{"--max-age", "0", "--account", owner, "calendar", "attendees", "list", ref, "--source-id", "1", "--limit", "1"}
		if history {
			args = append(args, "--history")
		}
		code, out, stderr = e.run(args...)
		if code != 0 || len(items(t, decode(t, out))) != 0 {
			t.Fatal("A->none->A or history/source selector bypassed captured owner", code, out, stderr)
		}
	}
	if code, _, stderr := show(owner, "attendee-observation:1"); code != 2 || errorOf(t, stderr)["code"] != "outlook_attendee_unknown" {
		t.Fatal("drifted ownership became qualified", code, stderr)
	}
}

func TestInstalledSteeringUnknownParityAndCorruptionPrecedence(t *testing.T) {
	e := newEnv(t)
	attendeeCLIArchive(t, e, 1, false)
	oldOpen := openAttendeeRead
	t.Cleanup(func() { openAttendeeRead = oldOpen })
	opens := 0
	openAttendeeRead = func(ctx context.Context, path string) (*store.Store, error) {
		opens++
		return oldOpen(ctx, path)
	}
	var previous map[string]any
	for _, account := range []string{"outlook/Main", "foreign/principal"} {
		for _, id := range []string{"attendee-observation:1", "attendee-observation:999"} {
			opens = 0
			code, out, stderr := e.run("--max-age", "0", "--account", account, "calendar", "attendees", "show", id)
			got := errorOf(t, stderr)
			if code != 2 || out != "" || got["code"] != "outlook_attendee_unknown" || opens != 1 {
				t.Fatal("scoped unknown retried or projected hidden evidence", code, out, stderr, opens)
			}
			requireOwnerSteering(t, got["fix"].(string))
			if previous != nil && !reflect.DeepEqual(got, previous) {
				t.Fatal("unlinked/foreign/absent classification leaked through error", previous, got)
			}
			previous = got
		}
	}
	e.exec(`update outlook_attendee_snapshots set result_json='{}'`)
	for _, account := range []string{"", "outlook/Main"} {
		args := []string{"--max-age", "0", "calendar", "attendees", "show", "attendee-observation:1"}
		if account != "" {
			args = append(args, "--account", account)
		}
		if code, _, stderr := e.run(args...); code != 3 || errorOf(t, stderr)["code"] != "outlook_attendee_archive_corrupt" {
			t.Fatal("existing relevant-current-source corruption audit was concealed", code, stderr)
		}
	}

	e.exec(`drop table outlook_link_state`)
	for _, id := range []string{"attendee-observation:1", "attendee-observation:999"} {
		if code, _, stderr := e.run("--max-age", "0", "--account", "outlook/Main", "calendar", "attendees", "show", id); code != 3 || errorOf(t, stderr)["code"] != "outlook_attendee_archive_corrupt" {
			t.Fatal("namespace corruption lost precedence to account/absence", code, stderr)
		}
	}
}

func TestInstalledSteeringAffectedHelp(t *testing.T) {
	e := newEnv(t)
	for _, suffix := range [][]string{{}, {"list"}, {"show"}, {"sources"}} {
		args := append([]string{"calendar", "attendees"}, suffix...)
		code, out, stderr := e.run(append(args, "--help")...)
		if code != 0 || stderr != "" {
			t.Fatal(code, stderr)
		}
		text := strings.Join(strings.Fields(out), " ")
		requireOwnerSteering(t, text)
		if len(suffix) == 0 || suffix[0] == "sources" {
			if !strings.Contains(text, "--account does not owner-filter") || !strings.Contains(text, "archive-wide diagnostics") {
				t.Error("source help did not distinguish diagnostic scope", out)
			}
		}
	}
}

func TestInstalledSteeringSyncStillRefusesAccount(t *testing.T) {
	e := newEnv(t)
	code, out, stderr := e.run("--account", "outlook/Main", "calendar", "attendees", "sync", "--source-id", "1")
	if code != 2 || out != "" || errorOf(t, stderr)["code"] != "usage" || strings.Contains(stderr, "changes inspection scope") {
		t.Fatal("read guidance changed the separate sync refusal", code, out, stderr)
	}
}
