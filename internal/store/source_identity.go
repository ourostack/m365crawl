package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// sourceIdentityKey + a source key marks that a sync of this machine wrote to the archive from that
// source. Each key is a hash the CLI derives from a random per-user machine ID, the source kind and
// its root, so the archive holds no machine, path or account in clear (SPEC section 4).
const sourceIdentityKey = "source_identity:"

// teamsSourceRun matches the sync_runs source of a Teams origin: <profile>|<origin>.
var teamsSourceRun = regexp.MustCompile(`^[^|]+\|https_teams\.(microsoft\.com|cloud\.microsoft)_\d+$`)

// SourceIdentity is what an archive records about the sources that feed it.
type SourceIdentity struct {
	// Recorded are the source keys syncs recorded.
	Recorded map[string]bool
	// TeamsSources are the Teams sources (<profile>|<origin>) a successful sync read: the only
	// evidence an archive from before source identities has of the machine that fed it.
	TeamsSources []string
}

// RecordSourceIdentity records keys, keeping the time each was first recorded.
func (s *Store) RecordSourceIdentity(ctx context.Context, keys []string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `insert into meta(key, value) select ? || j.value, ? from json_each(?) j where true on conflict(key) do nothing`,
		sourceIdentityKey, fmtTime(at), jsonList(keys))
	return err
}

// SourceIdentity reads the recorded source keys and the Teams sources the archive's sync history
// names.
func (s *Store) SourceIdentity(ctx context.Context) (SourceIdentity, error) {
	out := SourceIdentity{Recorded: map[string]bool{}}
	err := s.each(ctx, `select key from meta where key like 'source\_identity:%' escape '\'
union all select distinct '|' || source from sync_runs where status in `+successStatuses+` order by 1`, func(rs *sql.Rows) error {
		var key string
		err := rs.Scan(&key)
		if run, ok := strings.CutPrefix(key, "|"); ok && teamsSourceRun.MatchString(run) {
			out.TeamsSources = append(out.TeamsSources, run)
		} else if id, ok := strings.CutPrefix(key, sourceIdentityKey); ok {
			out.Recorded[id] = true
		}
		return err
	})
	return out, err
}

// jsonList is keys as a JSON array, [] for none.
func jsonList(keys []string) string {
	b, _ := json.Marshal(append([]string{}, keys...))
	return string(b)
}
