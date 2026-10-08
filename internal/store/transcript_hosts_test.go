package store

import (
	"context"
	"strings"
	"testing"
)

func TestTranscriptHosts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	hosts, err := s.TranscriptHosts(ctx)
	if err != nil || len(hosts) != 0 {
		t.Fatalf("empty archive: %v, %v", hosts, err)
	}
	tSeed(t, s)
	tDerive(t, s, tAcctA)
	// A second host with fewer fetchable parts, and a share-only part on a third that does not count.
	s.qExec(t, `update transcript_parts set host='other.sharepoint.example.invalid' where call_id='call-5'`)
	s.qExec(t, `update transcript_parts set host='share.sharepoint.example.invalid' where call_id='call-2'`)
	hosts, err = s.TranscriptHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(hosts, ","); got != "tenant.sharepoint.example.invalid,other.sharepoint.example.invalid" {
		t.Fatalf("hosts %s", got)
	}
	_ = s.Close()
	if _, err := s.TranscriptHosts(ctx); err == nil {
		t.Fatal("a closed archive answered")
	}
}
