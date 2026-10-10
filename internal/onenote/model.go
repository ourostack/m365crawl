package onenote

import "time"

type Entity struct {
	RowID                                                  int64
	Type                                                   int64
	GOID, GUID, GOSID, ParentGOID, GrandparentGOIDs, Title string
	ModifiedAt                                             time.Time
}

type Element struct {
	RowID, EntityRowID, Jcid int64
	GOID, Text, Kind         string
	HashtagMarkers           int
}

type Flag struct {
	RowID, ElementRowID, Type, Shape, Status int64
	Label                                    string
}

type Loss struct {
	Code  string
	Count int
}

type Result struct {
	Entities []Entity
	Elements []Element
	Flags    []Flag
	Losses   []Loss
	Ordering string
}

func filetime(ticks int64) (time.Time, bool) {
	if ticks <= 0 || ticks >= 2650467744000000000 {
		return time.Time{}, false
	}
	seconds := ticks/10000000 - 11644473600
	nanos := ticks % 10000000 * 100
	return time.Unix(seconds, nanos).UTC(), true
}
