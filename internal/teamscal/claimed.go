package teamscal

// Kind says which mapper a claimed object store feeds.
type Kind int

// The claimed store kinds.
const (
	// KindEvent is the calendar store: one record per event occurrence (MapEventRecord).
	KindEvent Kind = iota + 1
	// KindCalendarInternal holds sync bookkeeping (syncState, calendarSettings,
	// lastSuccesfulSyncTimestamp). It maps to no event; the store layer reads freshness from it.
	KindCalendarInternal
	// KindCatchUp is the meeting catch-up store, keyed by iCalUid (MapCatchUpRecord).
	KindCatchUp
	// KindRecap is the meeting recap store, keyed by recapId (MapRecapRecord).
	KindRecap
)

// ClaimedStore names one object store the calendar derivation reads from the archived records.
type ClaimedStore struct {
	// Manager is the database name's manager segment ("Teams:<manager>:react-web-client:...").
	Manager string
	Store   string
	Kind    Kind
}

var claimed = []ClaimedStore{
	{"calendar", "calendar", KindEvent},
	{"calendar", "calendar-internal-data", KindCalendarInternal},
	{"meetforwork-manager", "meetforwork-meeting-catch-up", KindCatchUp},
	{"meeting-recap-manager", "meeting-recap-catchup", KindRecap},
}

// ClaimedStores is the single list of stores the derivation reads. The generic-records test decoy
// ("calendar-manager" with store "events") is not Teams' calendar and is not in it.
func ClaimedStores() []ClaimedStore {
	return append([]ClaimedStore(nil), claimed...)
}

// Claimed reports the kind of a store, or false when the derivation does not read it.
func Claimed(manager, store string) (Kind, bool) {
	for _, c := range claimed {
		if c.Manager == manager && c.Store == store {
			return c.Kind, true
		}
	}
	return 0, false
}
