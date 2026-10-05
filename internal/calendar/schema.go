package calendar

// SchemaDDL creates the calendar tables; it is idempotent. There is no SQL view of the merged
// calendar: merging happens in Go (Agenda), because the tie-breaks need per-source freshness.
// Archive rule: nothing here deletes rows; a row a source stops reporting gets removed_at, a
// sticky timestamp, and reappearing clears it. Every table is partitioned by account_id (the
// "<tenantId>/<userId>" form; empty only for a source with no account).
const SchemaDDL = `
CREATE TABLE IF NOT EXISTS calendar_source_events (
  source TEXT NOT NULL,
  account_id TEXT NOT NULL DEFAULT '',
  event_key TEXT NOT NULL,
  composite_key TEXT NOT NULL,
  source_id TEXT NOT NULL,
  global_id TEXT NOT NULL DEFAULT '',
  ical_uid TEXT NOT NULL DEFAULT '',
  series_key TEXT NOT NULL DEFAULT '',
  event_type TEXT NOT NULL DEFAULT '',
  original_start TEXT,
  start_at TEXT NOT NULL DEFAULT '',
  end_at TEXT NOT NULL DEFAULT '',
  all_day INTEGER NOT NULL DEFAULT 0,
  start_date TEXT NOT NULL DEFAULT '',
  end_date TEXT NOT NULL DEFAULT '',
  time_zone TEXT NOT NULL DEFAULT '',
  time_zone_iana TEXT NOT NULL DEFAULT '',
  utc_offset TEXT NOT NULL DEFAULT '',
  subject TEXT NOT NULL DEFAULT '',
  organizer TEXT NOT NULL DEFAULT '',
  organizer_address TEXT NOT NULL DEFAULT '',
  is_organizer INTEGER NOT NULL DEFAULT 0,
  is_private INTEGER NOT NULL DEFAULT 0,
  cancelled INTEGER NOT NULL DEFAULT 0,
  response TEXT NOT NULL DEFAULT '',
  show_as TEXT NOT NULL DEFAULT '',
  is_online_meeting INTEGER NOT NULL DEFAULT 0,
  location TEXT NOT NULL DEFAULT '',
  last_modified TEXT,
  online_meeting_url TEXT NOT NULL DEFAULT '',
  short_join_url TEXT NOT NULL DEFAULT '',
  dial_in_conference_id TEXT NOT NULL DEFAULT '',
  dial_in_toll_number TEXT NOT NULL DEFAULT '',
  teams_thread_id TEXT NOT NULL DEFAULT '',
  attendees_json TEXT NOT NULL DEFAULT '',
  locations_json TEXT NOT NULL DEFAULT '',
  body_html TEXT NOT NULL DEFAULT '',
  body_text TEXT NOT NULL DEFAULT '',
  body_type TEXT NOT NULL DEFAULT '',
  body_preview TEXT NOT NULL DEFAULT '',
  attachments_json TEXT NOT NULL DEFAULT '',
  has_attachments INTEGER NOT NULL DEFAULT 0,
  categories_json TEXT NOT NULL DEFAULT '',
  recurrence_json TEXT NOT NULL DEFAULT '',
  reminder_minutes INTEGER,
  detail_raw_json TEXT NOT NULL DEFAULT '',
  detail_as_of TEXT,
  detail_seen_at TEXT,
  first_seen_at TEXT NOT NULL,
  seen_at TEXT NOT NULL,
  removed_at TEXT,
  PRIMARY KEY (source, account_id, event_key)
);
CREATE INDEX IF NOT EXISTS calendar_source_events_start ON calendar_source_events(account_id, start_at);
CREATE INDEX IF NOT EXISTS calendar_source_events_ical ON calendar_source_events(ical_uid);
CREATE INDEX IF NOT EXISTS calendar_source_events_composite ON calendar_source_events(composite_key);
CREATE INDEX IF NOT EXISTS calendar_source_events_thread ON calendar_source_events(teams_thread_id);
CREATE INDEX IF NOT EXISTS calendar_source_events_series ON calendar_source_events(series_key);
CREATE TABLE IF NOT EXISTS calendar_sources (
  source TEXT NOT NULL,
  account_id TEXT NOT NULL DEFAULT '',
  window_start TEXT NOT NULL,
  window_end TEXT NOT NULL,
  synced_at TEXT NOT NULL,
  cache_fresh_at TEXT NOT NULL,
  PRIMARY KEY (source, account_id)
);
CREATE TABLE IF NOT EXISTS calendar_covered_days (
  source TEXT NOT NULL,
  account_id TEXT NOT NULL DEFAULT '',
  day TEXT NOT NULL,
  first_verified_at TEXT NOT NULL,
  last_verified_at TEXT NOT NULL,
  PRIMARY KEY (source, account_id, day)
);
CREATE TABLE IF NOT EXISTS calendar_matches (
  event_key TEXT NOT NULL,
  source TEXT NOT NULL,
  account_id TEXT NOT NULL DEFAULT '',
  source_id TEXT NOT NULL,
  match_method TEXT NOT NULL,
  PRIMARY KEY (source, account_id, source_id)
);
CREATE INDEX IF NOT EXISTS calendar_matches_key ON calendar_matches(source, account_id, event_key);
CREATE TABLE IF NOT EXISTS calendar_recaps (
  account_id TEXT NOT NULL DEFAULT '',
  call_id TEXT NOT NULL,
  ical_uid TEXT NOT NULL DEFAULT '',
  recap_id TEXT NOT NULL DEFAULT '',
  link_method TEXT NOT NULL DEFAULT '',
  has_catchup INTEGER NOT NULL DEFAULT 0,
  has_recap INTEGER NOT NULL DEFAULT 0,
  headline TEXT NOT NULL DEFAULT '',
  short_summary TEXT NOT NULL DEFAULT '',
  outline TEXT NOT NULL DEFAULT '',
  summary_sections_json TEXT NOT NULL DEFAULT '',
  speakers_json TEXT NOT NULL DEFAULT '',
  topics_json TEXT NOT NULL DEFAULT '',
  recording_url TEXT NOT NULL DEFAULT '',
  recording_start_at TEXT,
  recording_end_at TEXT,
  duration_seconds INTEGER NOT NULL DEFAULT 0,
  is_missed INTEGER NOT NULL DEFAULT 0,
  meeting_start_at TEXT,
  meeting_end_at TEXT,
  expires_at TEXT,
  attendance_status TEXT NOT NULL DEFAULT '',
  attendees_count INTEGER NOT NULL DEFAULT 0,
  organizer_id TEXT NOT NULL DEFAULT '',
  has_conf_room_connected INTEGER NOT NULL DEFAULT 0,
  first_seen_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (account_id, call_id)
);
CREATE INDEX IF NOT EXISTS calendar_recaps_ical ON calendar_recaps(ical_uid);
CREATE TABLE IF NOT EXISTS calendar_recap_items (
  account_id TEXT NOT NULL DEFAULT '',
  call_id TEXT NOT NULL,
  item_key TEXT NOT NULL,
  kind TEXT NOT NULL,
  origin TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  owner_name TEXT NOT NULL DEFAULT '',
  speaker_name TEXT NOT NULL DEFAULT '',
  at TEXT,
  mentioned_by TEXT NOT NULL DEFAULT '',
  highlights_json TEXT NOT NULL DEFAULT '',
  ordinal INTEGER NOT NULL DEFAULT 0,
  first_seen_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  superseded_at TEXT,
  PRIMARY KEY (account_id, call_id, item_key)
);
CREATE INDEX IF NOT EXISTS calendar_recap_items_owner ON calendar_recap_items(owner_name);
CREATE INDEX IF NOT EXISTS calendar_recap_items_at ON calendar_recap_items(at);
CREATE INDEX IF NOT EXISTS calendar_recap_items_call ON calendar_recap_items(account_id, call_id);
`
