package store

// transcriptsSchemaDDL is the meeting transcript tables (schema version 7). Times are UTC text in
// timeLayout, like the rest of the archive.
//
// transcript_parts is derived: a sync rebuilds an account's rows from the recording and transcript
// notices in messages, so its rows may be deleted and written again. transcript_fetches and
// transcript_entries are not derived: they hold what a fetch brought back, keyed by the part key
// alone, and a rebuild of the parts never touches them. transcript_fts indexes the entries by the
// rowid of their transcript_entries row, so that table keeps its implicit rowid and the archive is
// never vacuumed.
const transcriptsSchemaDDL = `
create table if not exists transcript_parts(
  account_id text not null,
  call_id text not null,
  part_key text not null,
  thread_id text not null default '',
  message_id text not null default '',
  ordinal integer not null,
  starts_at text,
  duration_seconds real not null default 0,
  content_types text not null default '',
  chunk_index text not null default '',
  transcribe_only integer not null default 0,
  host text not null default '',
  site_root text not null default '',
  storage_kind text not null default '',
  drive_id text not null default '',
  item_id text not null default '',
  transcript_id text not null default '',
  share_url text not null default '',
  ref_quality text not null,
  meeting_ical_uid text not null default '',
  original_name text not null default '',
  sent_at text,
  primary key(account_id, call_id, part_key)
);
create index if not exists transcript_parts_thread on transcript_parts(thread_id);
create index if not exists transcript_parts_message on transcript_parts(message_id);
create index if not exists transcript_parts_starts on transcript_parts(starts_at desc);
create table if not exists transcript_fetches(
  account_id text not null,
  part_key text not null,
  state text not null,
  fetched_at text,
  http_status integer not null default 0,
  entry_count integer not null default 0,
  browser text not null default '',
  attempted_at text,
  primary key(account_id, part_key)
);
create table if not exists transcript_entries(
  account_id text not null,
  part_key text not null,
  ord integer not null,
  speaker text not null default '',
  start_ms integer,
  end_ms integer,
  text text not null default '',
  primary key(account_id, part_key, ord)
);
create virtual table if not exists transcript_fts using fts5(speaker, text);
`
