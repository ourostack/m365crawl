// Package teamscal maps Microsoft Teams calendar and meeting-recap records to the calendar core
// (internal/calendar). It holds pure functions only: they take the canonical JSON of one archived
// record and return core values, with no I/O, no archive access and no clock. The store layer
// selects the rows (ClaimedStores), calls the mappers and applies the results.
package teamscal

import "time"

// MapperVersion is raised whenever a mapping change alters derived calendar data. The archive
// stores it, and a different stored value makes the next sync re-map every claimed record.
const MapperVersion = 2

// RecapTimeTolerance is how far a store 2 recap's meeting start and end may differ from an
// event's and still link by time. It is a guess until the real-cache acceptance reports the
// distribution of the differences for recaps that link by iCalUID; set it from that histogram.
const RecapTimeTolerance = 60 * time.Second
