package calendar

// SchemaDDL creates the calendar tables; it is idempotent. There is no SQL view of the merged
// calendar: merging happens in Go (Agenda), because the tie-breaks need per-source freshness.
// Archive rule: nothing here deletes rows; a row a source stops reporting gets removed_at, a
// sticky timestamp, and reappearing clears it.
const SchemaDDL = `
CREATE TABLE IF NOT EXISTS calendar_source_events (
  source TEXT NOT NULL,
  event_key TEXT NOT NULL,
  composite_key TEXT NOT NULL,
  source_id TEXT NOT NULL,
  global_id TEXT NOT NULL DEFAULT '',
  original_start TEXT,
  start_at TEXT NOT NULL DEFAULT '',
  end_at TEXT NOT NULL DEFAULT '',
  all_day INTEGER NOT NULL DEFAULT 0,
  start_date TEXT NOT NULL DEFAULT '',
  end_date TEXT NOT NULL DEFAULT '',
  time_zone TEXT NOT NULL DEFAULT '',
  subject TEXT NOT NULL DEFAULT '',
  organizer TEXT NOT NULL DEFAULT '',
  attendees_json TEXT NOT NULL DEFAULT '',
  location TEXT NOT NULL DEFAULT '',
  online_meeting_url TEXT NOT NULL DEFAULT '',
  teams_thread_id TEXT NOT NULL DEFAULT '',
  series_key TEXT NOT NULL DEFAULT '',
  cancelled INTEGER NOT NULL DEFAULT 0,
  response TEXT NOT NULL DEFAULT '',
  show_as TEXT NOT NULL DEFAULT '',
  body_preview TEXT NOT NULL DEFAULT '',
  last_modified TEXT,
  seen_at TEXT NOT NULL,
  removed_at TEXT,
  PRIMARY KEY (source, event_key)
);
CREATE INDEX IF NOT EXISTS calendar_source_events_composite ON calendar_source_events(composite_key);
CREATE INDEX IF NOT EXISTS calendar_source_events_start ON calendar_source_events(source, start_at);
CREATE TABLE IF NOT EXISTS calendar_sources (
  source TEXT NOT NULL PRIMARY KEY,
  window_start TEXT NOT NULL,
  window_end TEXT NOT NULL,
  synced_at TEXT NOT NULL,
  cache_fresh_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS calendar_matches (
  event_key TEXT NOT NULL,
  source TEXT NOT NULL,
  source_id TEXT NOT NULL,
  match_method TEXT NOT NULL,
  PRIMARY KEY (source, source_id)
);
CREATE INDEX IF NOT EXISTS calendar_matches_key ON calendar_matches(source, event_key);
`
