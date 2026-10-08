package store

import (
	"context"
	"sort"
	"strings"
)

// TranscriptHosts lists the SharePoint hosts of the parts a fetch can ask for (their reference
// passes transcripts' checks, the host among them), the host with the most such parts first.
// transcripts signin opens the first one.
func (s *Store) TranscriptHosts(ctx context.Context) ([]string, error) {
	calls, _, err := s.TranscriptCalls(ctx, TranscriptFilter{})
	if err != nil {
		return nil, err
	}
	count := map[string]int{}
	var hosts []string
	for _, c := range calls {
		for _, p := range c.Parts {
			if !p.Fetchable {
				continue
			}
			h := strings.ToLower(p.Host)
			if count[h] == 0 {
				hosts = append(hosts, h)
			}
			count[h]++
		}
	}
	sort.Slice(hosts, func(i, j int) bool {
		if count[hosts[i]] != count[hosts[j]] {
			return count[hosts[i]] > count[hosts[j]]
		}
		return hosts[i] < hosts[j]
	})
	return hosts, nil
}
