package cli

import (
	"strings"
	"testing"
)

func TestShowCoverageShortTextDoesNotCertifySpeech(t *testing.T) {
	e := trEnv(t)
	m := trJSON(t, e, "transcripts", "show", "call-2")
	if m["coverage_scope"] != "discovered_parts" || m["complete"] != true {
		t.Fatalf("fetched discovered parts must remain complete without certifying speech: scope=%v complete=%v", m["coverage_scope"], m["complete"])
	}
	seg := m["segments"].([]any)[0].(map[string]any)
	if seg["notice_duration_seconds"] != float64(1800) {
		t.Fatalf("notice duration lost: %v", seg["notice_duration_seconds"])
	}
	entry := seg["entries"].([]any)[0].(map[string]any)
	if entry["end"] != "2026-11-10T10:00:02Z" || entry["text"] != "Short one today." || m["text_truncated"] != nil {
		t.Fatalf("short fetched text was changed: %v", entry)
	}
}

func TestShowCoverageUnknownNoticeDurationIsNull(t *testing.T) {
	e := trEnv(t)
	for _, duration := range []string{"0", "-1"} {
		e.exec(`update transcript_parts set duration_seconds=` + duration + ` where call_id='call-2'`)
		m := trJSON(t, e, "transcripts", "show", "call-2")
		seg := m["segments"].([]any)[0].(map[string]any)
		if got, present := seg["notice_duration_seconds"]; !present || got != nil {
			t.Errorf("unavailable notice duration %s must be present as null: present=%v value=%v", duration, present, got)
		}
		if m["complete"] != true {
			t.Fatal("unknown notice duration changed fetched-part completeness")
		}
	}
}

func TestShowCoverageUnavailablePartsKeepDurationAndPlace(t *testing.T) {
	e := trEnv(t)
	e.exec(`update transcript_parts set duration_seconds=12.75 where part_key='d:b!d1/P1'`)
	e.exec(`update transcript_parts set duration_seconds=0 where part_key='d:b!d1/P2'`)
	e.exec(`update transcript_parts set duration_seconds=900 where part_key='d:b!d1/P3'`)
	m := trJSON(t, e, "transcripts", "show", "call-1")
	if m["complete"] != false || m["coverage_scope"] != "discovered_parts" {
		t.Fatalf("partial call: complete=%v scope=%v", m["complete"], m["coverage_scope"])
	}
	segs := m["segments"].([]any)
	for i, want := range []any{float64(12.75), nil, float64(900)} {
		seg := segs[i].(map[string]any)
		if got, present := seg["notice_duration_seconds"]; !present || got != want || seg["ordinal"] != float64(i+1) {
			t.Errorf("part %d: duration=%v present=%v ordinal=%v", i+1, got, present, seg["ordinal"])
		}
	}
	missing := segs[2].(map[string]any)
	if missing["state"] != "not_fetched" || missing["reason"] != "not fetched yet; run m365crawl transcripts fetch call-1" || missing["fetched_at"] != nil || len(missing["entries"].([]any)) != 0 {
		t.Fatalf("unavailable part changed: %v", missing)
	}
	m = trJSON(t, e, "transcripts", "show", "call-3")
	unfetchable := m["segments"].([]any)[0].(map[string]any)
	if unfetchable["notice_duration_seconds"] != float64(1800) || unfetchable["state"] != "unfetchable" || unfetchable["reason"] != "only a sharing link is cached for this part; m365crawl cannot fetch it from ids" || m["complete"] != false {
		t.Fatalf("unfetchable notice metadata lost: %v", unfetchable)
	}
}

func TestShowCoverageProjectionAndHumanScope(t *testing.T) {
	e := trEnv(t)
	m := trJSON(t, e, "--fields", "coverage_scope", "transcripts", "show", "call-2")
	if len(m) != 3 || m["coverage_scope"] != "discovered_parts" || m["segments"] != nil || m["complete"] != nil {
		t.Fatalf("scope projection: %v", m)
	}
	m = trJSON(t, e, "--fields", "segments", "--max-text", "5", "transcripts", "show", "call-1")
	if m["coverage_scope"] != nil || m["complete"] != nil || m["text_truncated"] != true {
		t.Fatalf("segments projection: %v", m)
	}
	seg := m["segments"].([]any)[1].(map[string]any)
	entries := seg["entries"].([]any)
	bare := entries[1].(map[string]any)
	if seg["notice_duration_seconds"] != float64(1800) || bare["start"] != nil || bare["end"] != nil || bare["offset"] != nil || bare["text_truncated"] != true {
		t.Fatalf("duration passthrough changed unknown offsets or truncation: %v", seg)
	}
	code, out, errOut := e.tr("--format", "text", "transcripts", "show", "call-2")
	if code != 0 || !strings.Contains(out, "coverage: discovered parts only; recording speech coverage is unknown") || !strings.Contains(out, "── part 1 of 1") || !strings.HasSuffix(out, "source: archive · 1 of 1 parts fetched\n") {
		t.Fatalf("human scope, seam or footer: exit=%d stderr=%s\n%s", code, errOut, out)
	}
	code, out, errOut = e.tr("--format", "text", "--fields", "coverage_scope", "transcripts", "show", "call-2")
	if code != 0 || !strings.Contains(out, "discovered_parts") || strings.Contains(out, "Short one today.") || strings.Contains(out, "coverage: discovered parts only") || !strings.HasSuffix(out, "source: archive\n") {
		t.Fatalf("selected-field human output: exit=%d stderr=%s\n%s", code, errOut, out)
	}
}
