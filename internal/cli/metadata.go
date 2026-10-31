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
	for _, name := range []string{"drafts list", "drafts show"} {
		argv := append([]string{"m365crawl", "--json"}, strings.Fields(name)...)
		if name == "drafts show" {
			argv = append(argv, "<teams-draft-id>")
		}
		m.Commands[name] = control.Command{Argv: argv, JSON: true, Mutates: false, Title: name + " (archive-only cached own compose projection; no current-unsent guarantee)"}
	}
	for _, name := range []string{"status", "sync", "doctor", "search"} {
		m.Commands[name] = control.Command{Argv: []string{"m365crawl", "--json", name}, JSON: true, Mutates: name == "sync"}
	}
	for _, name := range []string{"enable", "disable", "sync", "coverage", "status"} {
		m.Commands["workiq "+name] = control.Command{Argv: []string{"m365crawl", "--json", "workiq", name},
			JSON: true, Mutates: name == "enable" || name == "disable" || name == "sync",
			Title: "workiq " + name + " (explicit optional server source; coverage/status retain no content or data cursors)"}
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
	for _, command := range []string{"notes sync", "notes list", "notes search", "notes show", "notes sources"} {
		argv := append([]string{"m365crawl", "--json"}, strings.Fields(command)...)
		switch command {
		case "notes search":
			argv = append(argv, "<words>")
		case "notes show":
			argv = append(argv, "<note-id>")
		}
		for _, command := range []string{"people", "people sync", "people show", "people sources"} {
			argv := append([]string{"m365crawl", "--json"}, strings.Fields(command)...)
			if command == "people show" {
				argv = append(argv, "<person-observation-id>")
			}
			m.Commands[command] = control.Command{Argv: argv, JSON: true, Mutates: command == "people sync",
				Title: command + " (native observations remain unresolved; show needs person-observation:ID)"}
		}
		m.Commands[command] = control.Command{Argv: argv, JSON: true, Mutates: command == "notes sync",
			Title: command + " (notes show needs an actual note:ID; notes search needs actual words)"}
	}
	for _, command := range []string{"docs sync", "docs list", "docs search", "docs show", "docs sources"} {
		argv := append([]string{"m365crawl", "--json"}, strings.Fields(command)...)
		switch command {
		case "docs search":
			argv = append(argv, "<words>")
		case "docs show":
			argv = append(argv, "<document-observation-id>")
		}
		m.Commands[command] = control.Command{Argv: argv, JSON: true, Mutates: command == "docs sync",
			Title: command + " (local metadata only; show needs document-observation:ID; no document opened or fetched)"}
	}
	for _, command := range []string{"loop sync", "loop list", "loop search", "loop show", "loop sources"} {
		argv := append([]string{"m365crawl", "--json"}, strings.Fields(command)...)
		switch command {
		case "loop search":
			argv = append(argv, "<words>")
		case "loop show":
			argv = append(argv, "<loop-channel-id>")
		}
		m.Commands[command] = control.Command{Argv: argv, JSON: true, Mutates: command == "loop sync",
			Title: command + " (cached partial channel snapshots from the archive; show needs loop-channel:ID; nothing fetched)"}
	}
	for _, command := range []string{"scout sync", "scout list", "scout search", "scout show", "scout sources"} {
		argv := append([]string{"m365crawl", "--json"}, strings.Fields(command)...)
		switch command {
		case "scout search":
			argv = append(argv, "<words>")
		case "scout show":
			argv = append(argv, "<scout-session-id>")
		}
		m.Commands[command] = control.Command{Argv: argv, JSON: true, Mutates: command == "scout sync",
			Title: command + " (opt-in Scout session history from the local OneDrive folder; sync never downloads; show needs scout-session:ID)"}
	}
	for _, command := range []string{"content capture", "content list", "content search", "content show", "content sources", "content fetch page", "content fetch stream", "content fetch engage-home", "content fetch engage-thread", "content signin"} {
		argv := append([]string{"m365crawl", "--json"}, strings.Fields(command)...)
		switch command {
		case "content capture":
			argv = append(argv, "--file", "<absolute-resident-office-file>")
		case "content search":
			argv = append(argv, "<words>")
		case "content show":
			argv = append(argv, "<cloud-local-text-or-engage-observation-id>")
		case "content fetch page", "content fetch stream", "content fetch engage-thread", "content signin":
			argv = append(argv, "<supported-url>")
		}
		m.Commands[command] = control.Command{Argv: argv, JSON: true, Mutates: command == "content capture" || strings.HasPrefix(command, "content fetch") || command == "content signin",
			Title: command + " (capture/fetch explicit only; reads offline; Windows cloud acquisition not yet qualified)"}
	}
	for _, command := range []string{"calendar attendees sync", "calendar attendees sources", "calendar attendees list", "calendar attendees show"} {
		argv := append([]string{"m365crawl", "--json"}, strings.Fields(command)...)
		switch command {
		case "calendar attendees list":
			argv = append(argv, "<event>")
		case "calendar attendees show":
			argv = append(argv, "<attendee-observation-id>")
		}
		m.Commands[command] = control.Command{Argv: argv, JSON: true, Mutates: command == "calendar attendees sync",
			Title: command + " (native source-selected detail, not displayed/current roster; reads always offline; show needs attendee-observation:ID)"}
	}
	m.Capabilities = []string{"doctor", "status", "sync", "watch", "search", "sql", "chats", "mail", "calendar", "native_calendar_attendee_evidence", "transcripts", "notes", "documents", "loop", "content", "scout", "workiq"}
	m.Privacy = control.Privacy{ContainsPrivateMessages: true, ExportsSecrets: false, LocalOnlyScopes: []string{"teams_cache", "outlook_store", "onenote_index", "outlook_people", "outlook_attendee_evidence", "office_json_metadata", "office_registry_metadata", "onedrive_usage_metadata", "explicit_resident_office_text", "scout_session_history"}}
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
