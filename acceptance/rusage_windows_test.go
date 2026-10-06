//go:build acceptance && windows

package acceptance

import "time"

// usage is this process's resource use so far; Windows does not report it here, so ok is false and
// the cost checks that need it say so and skip that part.
type usage struct {
	peakRSS int64
	cpu     time.Duration
	ok      bool
}

func processUsage() usage { return usage{} }
