package cli

import (
	"encoding/json"
	"strings"

	"github.com/openclaw/crawlkit/control"
)

// manifest is the crawlkit app manifest that `crawlctl discover` reads from `m365crawl metadata --json`.
func manifest() control.Manifest {
	m := control.NewManifest("m365crawl", "m365crawl", "m365crawl")
	m.Description = appDescription // the root help's sentence
	m.Paths.DefaultDatabase = "~/.m365crawl/m365crawl.db"
	m.Paths.ConfigEnv = "M365CRAWL_DB"
	for _, name := range []string{"status", "sync", "doctor", "search"} {
		m.Commands[name] = control.Command{Argv: []string{"m365crawl", "--json", name}, JSON: true, Mutates: name == "sync"}
	}
	m.Commands["calendar"] = control.Command{Argv: []string{"m365crawl", "--json", "calendar"}, JSON: true}
	m.Commands["overview"] = control.Command{Argv: []string{"m365crawl", "--json"}, JSON: true, Title: "overview (m365crawl with no command): what the archive holds per source and where to start"}
	for _, name := range []string{"mail list", "mail show", "mail thread", "mail folders", "mail unread"} {
		argv := append([]string{"m365crawl", "--json"}, strings.Fields(name)...)
		if name == "mail show" || name == "mail thread" {
			argv = append(argv, "<id>")
		}
		c := control.Command{Argv: argv, JSON: true}
		if name == "mail show" || name == "mail thread" {
			c.Title = name + " (<id> is a placeholder: use an id printed by mail list)"
		}
		m.Commands[name] = c
	}
	m.Commands["transcripts"] = control.Command{Argv: []string{"m365crawl", "--json", "transcripts"}, JSON: true,
		Title: "transcripts: recorded meetings and what of their transcripts is in the archive (offline)"}
	m.Commands["transcripts show"] = control.Command{Argv: []string{"m365crawl", "--json", "transcripts", "show", "<meeting>"}, JSON: true,
		Title: "transcripts show (<meeting> is a placeholder: use a call_id printed by transcripts)"}
	m.Capabilities = []string{"doctor", "status", "sync", "watch", "search", "sql", "chats", "mail", "calendar", "transcripts"}
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
