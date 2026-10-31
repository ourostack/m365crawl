package syncer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/outlookowacal"
)

// An OWA mail read records its own cache write time, so mail's cache_fresh_at does not depend on
// the calendar read of the same profile succeeding.
func TestOWAMailRecordsCacheWriteTimeWithoutTheCalendar(t *testing.T) {
	for name, block := range map[string]func(t *testing.T){
		"calendar fails": func(t *testing.T) {
			outlookOWACalendarCollect = func(context.Context, *indexeddb.Origin, string, bool) (outlookowacal.Result, error) {
				return outlookowacal.Result{}, errors.New("private calendar parser failure")
			}
		},
		"calendar origin unverified": func(t *testing.T) {
			resolve := peopleResolve
			t.Cleanup(func() { peopleResolve = resolve })
			peopleResolve = func(string) (string, error) { return "", errors.New("private origin failure") }
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, profile, _ := owaCalendarRunner(t, newDB(t))
			block(t)
			_, want, err := outlookowa.Fingerprint(profile)
			if err != nil || want.IsZero() {
				t.Fatalf("fixture write time: %v %v", want, err)
			}
			cal, mail := owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
			if cal.err == nil || mail.err != nil || !mail.decoded {
				t.Fatal("the calendar was not blocked while mail read")
			}
			want = want.Truncate(time.Millisecond) // the archive keeps milliseconds
			got, err := r.st.MailCacheFresh(context.Background(), outlookAccount("Default"))
			if err != nil || !got.Equal(want) {
				t.Fatalf("mail cache_fresh_at = %v %v, want %v", got, err, want)
			}
		})
	}
}
