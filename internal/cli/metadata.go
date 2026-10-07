package cli

import (
	"encoding/json"

	"github.com/openclaw/crawlkit/control"
)

// manifestDescription is the README's pitch sentence.
const manifestDescription = "Mirrors the Microsoft Teams desktop app's local cache, and optionally the new Outlook for Mac's calendar, into a SQLite archive on your Mac or Windows PC, with full-text search, unread state, mentions, the activity feed and a calendar with meeting recaps, so an AI agent can read your Teams history and agenda in milliseconds, offline and read-only."

// manifest is the crawlkit app manifest that `crawlctl discover` reads from `m365crawl metadata --json`.
func manifest() control.Manifest {
	m := control.NewManifest("m365crawl", "m365crawl", "m365crawl")
	m.Description = manifestDescription
	m.Paths.DefaultDatabase = "~/.m365crawl/m365crawl.db"
	m.Paths.ConfigEnv = "M365CRAWL_DB"
	for _, name := range []string{"status", "sync", "doctor", "search"} {
		m.Commands[name] = control.Command{Argv: []string{"m365crawl", "--json", name}, JSON: true, Mutates: name == "sync"}
	}
	m.Commands["calendar"] = control.Command{Argv: []string{"m365crawl", "--json", "calendar"}, JSON: true}
	m.Capabilities = []string{"doctor", "status", "sync", "watch", "search", "sql", "calendar"}
	m.Privacy = control.Privacy{ContainsPrivateMessages: true, ExportsSecrets: false, LocalOnlyScopes: []string{"teams_cache", "outlook_store"}}
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
