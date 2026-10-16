package syncer

import (
	"context"
	"errors"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

func finishSharedOutlook(ctx context.Context, s *store.Store, profile string, info outlookdesktop.Info, cleanup func(), snapshotErr error,
	people *pendingPeople, native *pendingAttendee, selections *outlookcal.AttendeeSelections, readID int64, calendarErr error, at time.Time) []outlookOutcome {
	if people == nil && native == nil {
		cleanup()
		return nil
	}
	var peopleStage stagedPeople
	if people != nil {
		peopleStage = stagedPeople{pending: *people, err: snapshotErr}
		if snapshotErr == nil {
			peopleStage = stageSharedPeople(ctx, info, *people)
		}
	}
	var nativeStage stagedAttendee
	if native != nil {
		cause := errors.Join(snapshotErr, calendarErr)
		if cause == nil && readID == 0 {
			cause = attendeeError("outlook_attendee_calendar_unassociated")
		}
		if cause == nil {
			_, cause = s.AutoLinkOutlook(ctx, at)
		}
		nativeStage = stageAttendee(ctx, info, selections, readID, *native, cause)
	}
	var common error
	if info.Path == "" {
		cleanup()
	} else {
		common = peopleCleanupVerified(info, cleanup)
	}
	out := []outlookOutcome{}
	if people != nil {
		out = append(out, finishStagedPeople(ctx, s, profile, info, peopleStage, common))
	}
	if native != nil {
		out = append(out, optionalAttendeeOutcome(profile, finishAttendee(ctx, s, nativeStage, common)))
	}
	return out
}
