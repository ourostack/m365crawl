package cli

import (
	"fmt"
	"io"
	"strings"
)

// sqlTable is one table of the archive and what it holds, in one line.
type sqlTable struct{ Name, Holds string }

// sqlTables is every table of the archive, in the order `sql --help` prints them. A test compares
// it with a fresh archive, so a table cannot be added without a line here. SQLite's own tables and
// the shadow tables of the full-text indexes (<name>_fts_data and so on) are left out.
var sqlTables = []sqlTable{
	{"accounts", "one row per Teams account: tenant_id, user_id, profile, when it was last synced"},
	{"conversations", "Teams chats, channels, teams and meeting chats"},
	{"messages", "Teams messages: sender_id, sender_name, sent_at, content_text, raw_json (the archived native message) and raw_digest"},
	{"people", "Teams people by tenant and id"},
	{"activity", "Teams activity-feed items (mentions, replies, reactions)"},
	{"message_app_observations", "application evidence of messages: one immutable row per message revision, with projection_json (a JSON string)"},
	{"message_app_current", "which message_app_observations row is current, and last good, for each message rowid"},
	{"records", "every other archived Teams record: source, database, store, key_json, value_json"},
	{"mail_messages", "Outlook mail messages"},
	{"mail_recipients", "recipients of a mail message, by the message's rowid"},
	{"mail_attachments", "attachment names, sizes and types of a mail message"},
	{"mail_folders", "Outlook mail folders"},
	{"mail_source_ids", "the native ids of mail messages, folders and attachments, and the keys the mail tables use for them"},
	{"mail_coverage", "how far back each mail folder was read, and when"},
	{"mail_absent", "mail that a read missed, counted until it is marked gone or evicted"},
	{"calendar_source_events", "calendar events, one row per occurrence and source"},
	{"calendar_sources", "the span of days each calendar source showed, and when it was read"},
	{"calendar_covered_days", "the days each calendar source covers"},
	{"calendar_recaps", "meeting recaps, one per call"},
	{"calendar_recap_items", "action items, notes and mentions of a recap"},
	{"calendar_account_links", "which Outlook account belongs to which Teams account"},
	{"calendar_matches", "which event of a source is which event key, and how it was matched"},
	{"calendar_losses", "what the calendar derivation could not use of a record"},
	{"transcript_parts", "the parts of each recorded meeting"},
	{"transcript_fetches", "what a transcript fetch brought back for a part"},
	{"transcript_entries", "fetched transcript text, one row per spoken entry"},
	{"note_entities", "OneNote indexed pages, sections and notebooks"},
	{"note_elements", "text and OCR elements of a note"},
	{"note_flags", "flags on the elements of a note"},
	{"document_observations", "Office document metadata and OneDrive usage observations"},
	{"document_capture_metadata", "one row per capture of a document source; members_capture is null when the capture holds its own member rows, else the earlier capture whose rows an unchanged capture shares"},
	{"document_capture_members", "which observations a document capture holds, stored once: the rows of capture C are those with capture_id = (select coalesce(members_capture, capture_id) from document_capture_metadata where capture_id = C)"},
	{"document_usage_relations", "which OneDrive usage recent row and document row of a capture share a native ID, stored under the capture that owns the member rows (the same coalesce as document_capture_members)"},
	{"document_locator_metadata", "the surface and application of each registered document source"},
	{"people_observations", "native Outlook people observations"},
	{"people_capture_metadata", "one row per capture of an Outlook people source"},
	{"people_capture_members", "which observations a people capture holds"},
	{"people_capture_links", "which Outlook profile and principal a people capture is linked to"},
	{"people_group_ambiguities", "native people groups a source holds ambiguously"},
	{"loop_snapshots", "cached Loop snapshots: one immutable row per registered record version"},
	{"loop_channels", "the channel text of a Loop snapshot (id is the N of loop-channel:N)"},
	{"loop_snapshot_losses", "what a Loop snapshot could not read, by code"},
	{"loop_locator_metadata", "the archived record each Loop source is registered from"},
	{"loop_attempt_evidence", "the record fingerprint each Loop projection attempt read"},
	{"cloud_locator_metadata", "canonical safe requests for explicit SharePoint/Stream retry; locator_id is registration_id"},
	{"cloud_observations", "immutable full native result_json; rowid is N in cloud-observation:N, never a native ID"},
	{"cloud_capture_members", "immutable observation membership of each explicit cloud capture"},
	{"cloud_capture_metadata", "requested versus current selected transcript and local completion clock of a cloud capture"},
	{"cloud_source_body_state", "last-good body observation and nullable current native selection per file/account instance"},
	{"cloud_fts", "retained supported cloud title/text/native metadata index"},
	{"local_text_locator_metadata", "immutable exact [path,requestedKind] registration metadata"},
	{"local_file_snapshots", "immutable complete source-byte digest, native evidence, policy and acquisition/admission clocks"},
	{"local_text_observations", "full structured result_json and own snapshot; rowid is N in local-text-observation:N"},
	{"local_text_capture_members", "observation membership of each explicit resident capture, including reuse"},
	{"local_text_source_state", "current archived file snapshot and last-good readable observation per native incarnation"},
	{"local_text_attempt_metadata", "explicit versus extension request origin per successful attempt"},
	{"local_text_references", "selected document origin per attempt; reference_locator_match is not global file identity"},
	{"local_text_fts", "retained paragraph/cell values and scoped native part/reference metadata"},
	{"engage_locator_metadata", "fixed Engage root registration, independent of native account evidence"},
	{"engage_source_metadata", "positive native Host/NetworkID/UserID, never a Teams account"},
	{"engage_sessions", "immutable complete bounded native Result, first clocks and source-bound representation digest"},
	{"engage_occurrences", "ordered duplicate thread-starter occurrences; rowid is N in engage-observation:N"},
	{"engage_capture_members", "every successful attempt's immutable session membership, including reuse"},
	{"engage_source_state", "own current archived session and last-good content session per verified instance; never complete feed"},
	{"engage_fts", "exact title/ordered block text and scoped native provenance per occurrence"},
	{"knowledge_locators", "registered notes, documents, people, Loop and explicit cloud sources"},
	{"knowledge_attempts", "every capture or projection attempt of a registered source"},
	{"knowledge_sources", "the admitted and complete capture of each registered source"},
	{"message_fts", "full-text index of messages (rowid is the message's rowid)"},
	{"conversation_fts", "full-text index of conversation titles"},
	{"mail_fts", "full-text index of mail"},
	{"transcript_fts", "full-text index of transcript entries"},
	{"note_fts", "full-text index of notes"},
	{"document_fts", "full-text index of document metadata"},
	{"loop_fts", "full-text index of Loop channels (path, text)"},
	{"sync_runs", "every sync run: when, status, counts and omissions"},
	{"meta", "key and value settings of the archive, such as mapper versions"},
	{"schema_migrations", "the schema versions applied to this archive"},
	{"typed_memo", "internal read memory of the sync; not part of the contract"},
}

const sqlHelp = "The archive is opened read-only, so nothing a query does can change it. One statement per run: SELECT, WITH, EXPLAIN or VALUES. PRAGMA statements are refused; read a table's columns with the pragma_table_info() table function, as shown below.\n" +
	"For immutable content full_text_sql follow-ups, keep the same explicit --db and pass --max-age 0: SQL otherwise retains the generic implicit freshness workflow before its read-only query.\n" +
	"Blob columns print as lowercase hex. messages.raw_digest is the SHA-256 of the archived raw JSON (the exact bytes of messages.raw_json), stored as 32 bytes and printed as 64 lowercase hex characters; it is null when no raw JSON is archived. raw_digest in app_observation and in message_app_observations is the same digest as lowercase hex text.\n" +
	"Archive clocks are UTC text. Engage canonical Result clocks preserve original native offsets; engage_occurrences.starter_modified_at is checked derived evidence only, may have extended UTC years and must not be used as an RFC3339 parser, lexical ordering or filter key. A column named *_json holds JSON as text: a query returns it as a JSON string, to decode a second time or to read with json_extract().\n"

func (sqlCmd) Help() string { return sqlHelp }

// writeSQLHelp prints the archive's tables and how to see their columns, after the rest of the
// help of sql.
func writeSQLHelp(w io.Writer, cmd string) {
	if cmd != "sql" {
		return
	}
	width := 0
	for _, t := range sqlTables {
		width = max(width, len(t.Name))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nTables (%d, without SQLite's own and the full-text shadow tables):\n", len(sqlTables))
	hang := strings.Repeat(" ", width+4)
	for _, t := range sqlTables {
		b.WriteString(wrapHanging("  "+t.Name+strings.Repeat(" ", width-len(t.Name))+"  ", hang, t.Holds))
	}
	b.WriteString("\nColumns of a table (--limit 200 fits the widest table; the default is 50):\n  m365crawl sql \"select name, type from pragma_table_info('messages')\" --limit 200\n" +
		"Tables of this archive, and the statement that created one:\n  m365crawl sql \"select name from sqlite_master where type='table' order by name\" --limit 200\n" +
		"  m365crawl sql \"select sql from sqlite_master where name='messages'\"\n")
	_, _ = io.WriteString(w, b.String())
}
