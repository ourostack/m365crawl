package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

type attendeeIndexObserver struct {
	knowledgeReader
	indexes *int
}

func (q attendeeIndexObserver) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.HasPrefix(query, "select rowid,source_id,ordinal,") {
		*q.indexes++
	}
	return q.knowledgeReader.QueryContext(ctx, query, args...)
}

func TestAttendeeMembershipIndexesStreamOnce(t *testing.T) {
	s := newStore(t)
	a := attendeeModel(t, "A", 3)
	admitAttendeeFixture(t, s, a, base)
	admitAttendeeFixture(t, s, a, base)
	ctx := context.Background()
	if err := s.inTx(ctx, func(tx *sql.Tx) error {
		count := 0
		if err := auditAttendeeMetadata(ctx, attendeeIndexObserver{tx, &count}); err != nil {
			return err
		}
		if count != 2 {
			t.Fatalf("one immutable snapshot indexes streamed %d times, want two (occurrences and bindings)", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
