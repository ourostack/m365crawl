package cli

import (
	"encoding/json"

	"github.com/openclaw/crawlkit/control"
)

// manifestDescription is the README's pitch sentence.
const manifestDescription = "Mirrors the Microsoft Teams desktop app's local cache into a SQLite archive on your Mac, with full-text search, unread state, mentions and the activity feed, so an AI agent can read your Teams history in milliseconds, offline and read-only."

// manifest is the crawlkit app manifest that `crawlctl discover` reads from `teamscrawl metadata --json`.
func manifest() control.Manifest {
	m := control.NewManifest("teamscrawl", "teamscrawl", "teamscrawl")
	m.Description = manifestDescription
	m.Paths.DefaultDatabase = "~/.teamscrawl/teamscrawl.db"
	m.Paths.ConfigEnv = "TEAMSCRAWL_DB"
	for _, name := range []string{"status", "sync", "doctor", "search"} {
		m.Commands[name] = control.Command{Argv: []string{"teamscrawl", "--json", name}, JSON: true, Mutates: name == "sync"}
	}
	m.Capabilities = []string{"doctor", "status", "sync", "watch", "search", "sql"}
	m.Privacy = control.Privacy{ContainsPrivateMessages: true, ExportsSecrets: false, LocalOnlyScopes: []string{"teams_cache"}}
	return m
}

// metadataCmd prints the crawlkit manifest. Like skill it has one fixed output (JSON here) whatever
// --format says, needs no archive or Teams cache, and never runs the implicit sync.
type metadataCmd struct{}

func (metadataCmd) Run(rt *runtime) error {
	enc := json.NewEncoder(rt.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(manifest())
}
