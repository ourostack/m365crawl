package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAttendeeCalendarReadIdentity(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	var previous int64
	for _, at := range []time.Time{base, base, base.Add(-time.Hour), {}, time.Time{}.In(time.FixedZone("source", -7*3600))} {
		b := OutlookBatch{Account: "outlook/Main", Origin: "/synthetic/root/Main/HxStore.hxd", At: at, FreshAt: base, Zone: time.UTC, Stamp: "same"}
		result, err := s.CommitOutlook(ctx, b, outlookRun)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := s.OutlookReadReceipt(ctx, b.Account)
		if err != nil || receipt.ID <= previous || result.OutlookReadID != receipt.ID ||
			receipt.At.Format(time.RFC3339Nano) != at.Format(time.RFC3339Nano) || receipt.Origin != b.Origin || receipt.MapperStamp != "same" {
			t.Fatalf("decode identity/offset was derived from time or content: %+v %+v %v", result, receipt, err)
		}
		previous = receipt.ID
	}
	before := rowCount(t, s, "select count(*) from outlook_calendar_reads")
	if err := s.RecordRun(ctx, Run{StartedAt: base, FinishedAt: base, Source: "outlook|Main", Status: "unchanged"}); err != nil {
		t.Fatal(err)
	}
	if after := rowCount(t, s, "select count(*) from outlook_calendar_reads"); after != before {
		t.Fatal("unchanged check allocated a decode")
	}
	s.qExec(t, `create trigger reject_attendee_read_run before insert on sync_runs begin select raise(abort,'synthetic'); end`)
	failed, err := s.CommitOutlook(ctx, OutlookBatch{Account: "outlook/Main", At: base, Zone: time.UTC}, outlookRun)
	if err == nil {
		t.Fatal("failed commit succeeded")
	}
	if failed.OutlookReadID != 0 {
		t.Fatal("failed transaction returned an unpublished decode identity")
	}
	if after := rowCount(t, s, "select count(*) from outlook_calendar_reads"); after != before {
		t.Fatal("failed commit published a decode")
	}
}

func TestAttendeeOriginAdmission(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	check := func(account, origin string, want AttendeeOriginState) {
		t.Helper()
		got, err := s.CheckAttendeeOrigin(ctx, account, origin)
		if err != nil || got != want {
			t.Fatalf("origin=%s want=%s err=%v", got, want, err)
		}
	}
	check("outlook/Main", "/synthetic/one/Main/HxStore.hxd", "new")
	commitOutlook(t, s, time.UTC, "same")
	check("outlook/Main", "/synthetic/one/Main/HxStore.hxd", "unknown")
	for _, origin := range []string{"/synthetic/one/Main/HxStore.hxd", "/synthetic/two/Main/HxStore.hxd", "/synthetic/one/Main/HxStore.hxd"} {
		if _, err := s.CommitOutlook(ctx, OutlookBatch{Account: "outlook/Main", Origin: origin, At: base, Zone: time.UTC}, outlookRun); err != nil {
			t.Fatal(err)
		}
		check("outlook/Main", origin, "matched")
		check("outlook/Main", "/synthetic/foreign/Main/HxStore.hxd", "conflict")
	}
	check("outlook/Main", "", "unknown")
}

func TestAttendeeLinkRevision(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	commitOutlook(t, s, time.UTC, "same")
	revision := func() int64 {
		t.Helper()
		id, err := attendeeLinkRevision(ctx, s.db, "outlook/Main")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	if revision() != 0 {
		t.Fatal("migration/new-account baseline is not zero")
	}
	s.qExec(t, `insert into calendar_account_links(source,account_id,principal_id,method,linked_at)
values('outlook','outlook/Main','tenant/A','config','2031-03-01T00:00:00Z')`)
	first := revision()
	s.qExec(t, `update calendar_account_links set linked_at=linked_at`)
	if revision() != first {
		t.Fatal("no-op update advanced scope")
	}
	s.qExec(t, `update calendar_account_links set unlinked_at='2031-03-01T00:00:00Z'`)
	second := revision()
	s.qExec(t, `update calendar_account_links set unlinked_at=null`)
	if first <= 0 || second <= first || revision() <= second {
		t.Fatal("A->none->A regained an old scope")
	}
	s.qExec(t, `delete from calendar_account_links where source='outlook'`)
	if revision() <= second {
		t.Fatal("delete failed to advance scope")
	}
}

func TestAttendeeLinkFrontierIntegrity(t *testing.T) {
	for _, fault := range []string{"missing_state", "lost_tail", "wrong_account", "missing_trigger", "altered_trigger"} {
		t.Run(fault, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			commitOutlook(t, s, time.UTC, "same")
			s.qExec(t, `insert into calendar_account_links(source,account_id,principal_id,method,linked_at)
values('outlook','outlook/Main','tenant/A','config','2031-03-01T00:00:00Z')`)
			switch fault {
			case "missing_state":
				s.qExec(t, `delete from outlook_link_state where account_id='outlook/Main'`)
			case "lost_tail":
				s.qExec(t, `pragma foreign_keys=off`)
				s.qExec(t, `delete from outlook_link_events where rowid=(select event_id from outlook_link_state where account_id='outlook/Main')`)
			case "wrong_account":
				s.qExec(t, `pragma foreign_keys=off`)
				s.qExec(t, `update outlook_link_events set account_id='outlook/Other'`)
			case "missing_trigger", "altered_trigger":
				s.qExec(t, `drop trigger outlook_attendee_link_insert`)
				if fault == "altered_trigger" {
					s.qExec(t, `create trigger outlook_attendee_link_insert before insert on calendar_account_links begin select 1; end`)
				}
			}
			if _, err := s.OutlookReadReceipt(ctx, "outlook/ZeroHits"); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
				t.Fatalf("zero-hit read hid frontier/trigger corruption: %v", err)
			}
			if _, err := s.CommitOutlook(ctx, OutlookBatch{Account: "outlook/Main", At: base, Zone: time.UTC}, outlookRun); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
				t.Fatalf("writer repaired frontier/trigger corruption: %v", err)
			}
		})
	}
}
