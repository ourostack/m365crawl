package store

import (
	"context"

	"github.com/ourostack/m365crawl/internal/transcripts"
)

// TranscriptHosts lists the SharePoint hosts of the parts a fetch can ask for by ids, the host with
// the most such parts first. transcripts signin opens the first one.
func (s *Store) TranscriptHosts(ctx context.Context) ([]string, error) {
	return s.callIDs(ctx, `select lower(host) from transcript_parts where ref_quality=? and host<>''
	  group by lower(host) order by count(*) desc, lower(host)`, transcripts.RefDriveItem)
}
