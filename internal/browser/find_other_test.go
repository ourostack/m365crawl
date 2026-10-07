//go:build !darwin && !windows

package browser

import "testing"

func TestPlatformCandidatesOther(t *testing.T) {
	if platformCandidates(KindEdge) != nil {
		t.Fatal("no known locations on this platform")
	}
}
