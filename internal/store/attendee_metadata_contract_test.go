package store

import (
	"context"
	"errors"
	"testing"
)

func TestAttendeeGenericFrontierIntegrity(t *testing.T) {
	for _, statement := range []string{
		`update knowledge_locators set latest_attempt=null`,
		`update knowledge_locators set last_admitted=null`,
		`update knowledge_sources set source_observed_at='2030-01-01T00:00:00Z'`,
		`update knowledge_attempts set reader_version=2`,
		`update knowledge_attempts set mapper_version=2`,
	} {
		t.Run(statement, func(t *testing.T) {
			s := newStore(t)
			admitAttendeeFixture(t, s, attendeeModel(t, "A", 3), base)
			s.qExec(t, statement)
			if _, err := s.AttendeeSources(context.Background(), 0); !errors.Is(err, ErrAttendeeArchiveCorrupt) {
				t.Fatalf("contradictory frontier accepted: %v", err)
			}
		})
	}
}
