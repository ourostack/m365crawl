package syncer

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestOWAProfileLocalAllDayLiteralSQLAndRequalification(t *testing.T) {
	db := newDB(t)
	collect := outlookOWACalendarCollect
	r, _, clock := owaCalendarRunner(t, db)
	outlookOWACalendarCollect = collect
	profile := owaProfile(owaRoot(t, "calendar"))
	ctx := context.Background()
	cal, mail := owaKinds(t, r.outlookOWAProfile(ctx, profile))
	if cal.err != nil || mail.err != nil || !cal.decoded || !mail.decoded || cal.report.Status != StatusOK {
		t.Fatal("genuine local-date calendar/mail fixture did not commit cleanly")
	}
	raw := openRaw(t, db)
	assertDates := func() string {
		t.Helper()
		var count int
		if err := raw.QueryRow(`select count(*) from calendar_source_events where source='outlook' and account_id='outlook/Default'`).Scan(&count); err != nil || count != 2 {
			t.Fatal("literal calendar SQL census differs")
		}
		var start, end, firstDate, lastDate, zone, iana, firstSeen string
		var allDay sql.NullInt64
		err := raw.QueryRow(`select start_at,end_at,all_day,start_date,end_date,time_zone,time_zone_iana,first_seen_at
			from calendar_source_events where source='outlook' and account_id='outlook/Default' and source_id='bb22'`).
			Scan(&start, &end, &allDay, &firstDate, &lastDate, &zone, &iana, &firstSeen)
		if err != nil || start != "2031-03-06T08:00:00.000Z" || end != "2031-03-07T08:00:00.000Z" ||
			!allDay.Valid || allDay.Int64 != 1 || firstDate != "2031-03-06" || lastDate != "2031-03-07" ||
			zone != "Pacific Standard Time" || iana != "America/Los_Angeles" {
			t.Fatalf("literal local-date SQL contract differs: start=%s end=%s all_day=%v dates=%s/%s zone=%s iana=%s err=%v", start, end, allDay, firstDate, lastDate, zone, iana, err)
		}
		return firstSeen
	}
	firstSeen := assertDates()
	state, err := r.st.OutlookState(ctx, "Default", outlookKey("Default"), "outlook/Default")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := r.st.OutlookReadReceipt(ctx, "outlook/Default")
	if err != nil {
		t.Fatal(err)
	}
	zone := calendarZone()
	_, err = r.st.CommitOutlook(ctx, store.OutlookBatch{
		Account: "outlook/Default", Origin: receipt.Origin, At: *clock, FreshAt: *clock, Zone: zone,
		Stamp: store.OutlookStamp(2, zone), PreserveIdentity: true,
	}, func(store.CalendarResult) store.Run {
		return store.Run{Source: outlookKey("Default"), StartedAt: *clock, FinishedAt: *clock, Fingerprint: state.Fingerprint, Status: StatusOK}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`update calendar_source_events set all_day=null,start_date='',end_date=''
		where source='outlook' and account_id='outlook/Default' and source_id='bb22'`); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	cal, mail = owaKinds(t, r.outlookOWAProfile(ctx, profile))
	if cal.err != nil || mail.err != nil || !cal.decoded || mail.decoded || mail.report.Status != StatusUnchanged {
		t.Fatal("stale calendar dates were not requalified independently of unchanged mail")
	}
	if assertDates() != firstSeen {
		t.Fatal("calendar requalification reset first-seen history")
	}
}
