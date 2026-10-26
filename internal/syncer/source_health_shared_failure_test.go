package syncer

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestSourceHealthSharedOutlookDiscoveryFailureAlsoCoversMail(t *testing.T) {
	for _, source := range []string{"outlook", "outlook|Unreadable"} {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, newDB(t))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			r := &runner{st: st, o: Options{OutlookEnabled: true}}
			rep := Report{Status: StatusPartial, Sources: []SourceReport{{Source: source, Status: StatusUnavailable, Error: &SourceError{Code: "no_full_disk_access"}}}}
			if err := r.recordSourceHealth(ctx, &rep, nil); err != nil {
				t.Fatal(err)
			}
			mail := []store.SourceHealth{}
			for _, h := range rep.FailedSourceHealth {
				if h.Source == "outlook_mail" {
					mail = append(mail, h)
				}
			}

			if len(mail) != 1 || mail[0].LastErrorCode != "no_full_disk_access" {
				t.Fatalf("shared discovery failure hid mail health: %+v", rep.FailedSourceHealth)
			}
			if source == "outlook" && mail[0].AccountID != nil {
				t.Fatal("root-wide refusal invented a profile")
			}
			if source != "outlook" && (mail[0].AccountID == nil || *mail[0].AccountID != "outlook/Unreadable") {
				t.Fatalf("named unreadable profile lost identity: %+v", mail[0])
			}
		})
	}
}

func TestSourceHealthCalendarOnlyFailureDoesNotInventMailFailure(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	r := &runner{st: st, o: Options{OutlookEnabled: true}}
	rep := Report{Status: StatusPartial, Sources: []SourceReport{{Source: "outlook|Main", Status: StatusFailed, Error: &SourceError{Code: "outlook_layout_unsupported"}}}}
	if err := r.recordSourceHealth(ctx, &rep, nil); err != nil {
		t.Fatal(err)
	}
	for _, h := range rep.FailedSourceHealth {
		if h.Source == "outlook_mail" {
			t.Fatal("a calendar-only refusal became a mail read failure")
		}
	}
}
