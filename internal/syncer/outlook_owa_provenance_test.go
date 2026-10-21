package syncer

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestOWACalendarPreProvenanceReadRequalifiesCalendarOnly(t *testing.T) {
	testOWACalendarPriorMapperRequalifies(t, 1)
}

func TestOWACalendarPreLocalDateReadRequalifiesCalendarOnly(t *testing.T) {
	testOWACalendarPriorMapperRequalifies(t, 2)
}

func TestOWACalendarPrePrivacyReadRequalifiesCalendarOnly(t *testing.T) {
	testOWACalendarPriorMapperRequalifies(t, 3)
}

func testOWACalendarPriorMapperRequalifies(t *testing.T, priorVersion int) {
	t.Helper()
	r, profile, clock := owaCalendarRunner(t, newDB(t))
	ctx := context.Background()
	cal, mail := owaKinds(t, r.outlookOWAProfile(ctx, profile))
	if cal.err != nil || mail.err != nil {
		t.Fatal("initial synthetic source read failed")
	}
	account := outlookAccount("Default")
	prior, err := r.st.OutlookState(ctx, "Default", outlookKey("Default"), account)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := r.st.OutlookReadReceipt(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	zone := calendarZone()
	_, err = r.st.CommitOutlook(ctx, store.OutlookBatch{
		Account: account, Origin: receipt.Origin, At: *clock, FreshAt: *clock, Zone: zone,
		Stamp: store.OutlookStamp(priorVersion, zone), PreserveIdentity: true,
	}, func(store.CalendarResult) store.Run {
		return store.Run{Source: outlookKey("Default"), StartedAt: *clock, FinishedAt: *clock, Fingerprint: prior.Fingerprint, Status: StatusOK}
	})
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	cal, mail = owaKinds(t, r.outlookOWAProfile(ctx, profile))
	if cal.err != nil || mail.err != nil || !cal.decoded || mail.decoded || mail.report.Status != StatusUnchanged {
		t.Fatal("unchanged fingerprint retained stale calendar derivation or reread healthy mail")
	}
}
