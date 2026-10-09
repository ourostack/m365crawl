package store

import (
	"context"
	"sort"
	"strings"
)

// TranscriptHost is a SharePoint host the archive's fetchable parts live on, with the site root of
// its first such part (such as /personal/<user>), where a browser can open the host and stay on it.
type TranscriptHost struct {
	Host, SiteRoot string
}

// TranscriptHosts lists the SharePoint hosts of the parts a fetch can ask for (their reference
// passes transcripts' checks, the host among them), the host with the most such parts first.
// transcripts signin opens the first one, on its site.
func (s *Store) TranscriptHosts(ctx context.Context) ([]TranscriptHost, error) {
	calls, _, err := s.TranscriptCalls(ctx, TranscriptFilter{})
	if err != nil {
		return nil, err
	}
	count := map[string]int{}
	var hosts []TranscriptHost
	for _, c := range calls {
		for _, p := range c.Parts {
			if !p.Fetchable {
				continue
			}
			h := strings.ToLower(p.Host)
			if count[h] == 0 {
				hosts = append(hosts, TranscriptHost{Host: h, SiteRoot: p.SiteRoot})
			}
			count[h]++
		}
	}
	sort.SliceStable(hosts, func(i, j int) bool {
		if count[hosts[i].Host] != count[hosts[j].Host] {
			return count[hosts[i].Host] > count[hosts[j].Host]
		}
		return hosts[i].Host < hosts[j].Host
	})
	return hosts, nil
}
