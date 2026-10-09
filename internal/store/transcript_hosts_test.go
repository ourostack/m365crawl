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
	// A drive item on a host that is not SharePoint's is not offered.
	s.qExec(t, `update transcript_parts set host='attacker.example' where call_id='call-1' and ordinal=1`)
	s.qExec(t, `update transcript_parts set host='attacker.example' where call_id='call-1' and ordinal=2`)
	// Each host comes with the site of its first fetchable part.
	s.qExec(t, `update transcript_parts set site_root='/personal/dee_example_invalid' where call_id='call-5'`)
	hosts, err = s.TranscriptHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0] != (TranscriptHost{Host: "other.sharepoint.example.invalid", SiteRoot: "/personal/dee_example_invalid"}) {
		t.Fatalf("hosts %+v", hosts)
	}
	// Hosts with as many parts each go by name; then the host with the most parts comes first.
	s.qExec(t, `update transcript_parts set host='b.sharepoint.example.invalid' where call_id='call-1' and ordinal=1`)
	s.qExec(t, `update transcript_parts set host='a.sharepoint.example.invalid' where call_id='call-1' and ordinal=2`)
	for _, step := range []struct{ update, want string }{
		{"", "a.sharepoint.example.invalid,b.sharepoint.example.invalid,other.sharepoint.example.invalid"},
		{`update transcript_parts set host='B.sharepoint.example.invalid' where call_id='call-1' and ordinal=2`, "b.sharepoint.example.invalid,other.sharepoint.example.invalid"},
	} {
		if step.update != "" {
			s.qExec(t, step.update)
		}
		hosts, err = s.TranscriptHosts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, h := range hosts {
			names = append(names, h.Host)
		}
		if got := strings.Join(names, ","); got != step.want {
			t.Fatalf("hosts %s, want %s", got, step.want)
		}
	}
	_ = s.Close()
	if _, err := s.TranscriptHosts(ctx); err == nil {
		t.Fatal("a closed archive answered")
	}
}
