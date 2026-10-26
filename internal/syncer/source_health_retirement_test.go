package syncer

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestSourceHealthRetirementRequiresDiscoveryEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prior   []string
		rep     Report
		err     error
		outlook bool
		want    []string
	}{
		{
			name:  "cancelled before Outlook",
			prior: []string{"outlook|Gone|mail"},
			rep:   Report{Status: StatusPartial},
			err:   context.Canceled, outlook: true,
			want: []string{"outlook|Gone|mail"},
		},
		{
			name:  "removed Teams origin",
			prior: []string{"old|teams"},
			rep:   Report{Status: StatusOK, Sources: []SourceReport{{Source: "current|teams", Status: StatusOK}}},
			want:  []string{},
		},
		{
			name:  "Teams no longer installed",
			prior: []string{"old|teams"},
			err:   &errs.Coded{Code: errs.CodeTeamsNotInstalled},
			want:  []string{},
		},
		{
			name:    "explicit Outlook root now empty",
			prior:   []string{"outlook", "outlook|Gone", "outlook|Gone|mail"},
			rep:     Report{Status: StatusPartial, Sources: []SourceReport{{Source: "outlook", Status: StatusFailed, Error: &SourceError{Code: CodeNoOutlookProfiles}}}},
			outlook: true,
			want:    []string{"outlook"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, newDB(t))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			now := time.Now().UTC()
			for _, key := range tc.prior {
				if err := st.RecordRun(ctx, store.Run{Source: key, Status: StatusFailed, ErrorCode: "internal", StartedAt: now, FinishedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			r := &runner{st: st, o: Options{OutlookEnabled: tc.outlook}}
			if err := r.recordSourceHealth(ctx, &tc.rep, tc.err); err != nil {
				t.Fatal(err)
			}
			health, err := st.SourceHealth(ctx)
			if err != nil {
				t.Fatal(err)
			}
			keys := []string{}
			for _, h := range health {
				keys = append(keys, h.SourceKey)
			}
			if !reflect.DeepEqual(keys, tc.want) {
				t.Fatalf("retirement lost or invented discovery evidence: %v, want %v", keys, tc.want)
			}
		})
	}
}
