package store

// SchemaVersion is the archive schema version recorded through crawlkit's schema_migrations.
// Version 3 added the records table. Version 4 added read memory: records.raw_digest and
// records.value_redacted, and the typed_memo table (see memo.go).
const SchemaVersion = 4

// DerivationVersion numbers how the mappers turn a Teams record into the archive's derived
// fields: message text and sender name, and the display name of a conversation. It is stored as
// meta.derivation_version, and an archive with no such row was written by alpha.1, version 1.
// Version 2 added readable text for cards, call events and thread activity, sender names from
// fromDisplayNameInToken, and member-list names for untitled chats. Raise it whenever a mapper
// change alters a derived field, and make Rederive recompute the new field from raw_json.
const DerivationVersion = 2

// schemaDDL is applied on every Open; every statement is idempotent. Timestamps are UTC text in
// timeLayout so they sort and compare as strings. Messages and conversations are rowid tables
// because the FTS tables reuse the owning row's rowid, which makes index maintenance a primary
// key lookup instead of a scan.
const schemaDDL = `
create table if not exists accounts(
  tenant_id text not null,
  user_id text not null,
  profile text not null default '',
  locale text not null default '',
  first_seen_at text,
  last_synced_at text,
  primary key(tenant_id, user_id)
);
create table if not exists conversations(
  tenant_id text not null,
  user_id text not null,
  id text not null,
  kind text not null default '',
  title text not null default '',
  topic text not null default '',
  display_name text not null default '',
  team_id text not null default '',
  parent_id text not null default '',
  members_json text,
  last_message_at text,
  read_horizon_at text,
  read_horizon_client_message_id text not null default '',
  favorite integer not null default 0,
  raw_json text,
  content_hash text not null default '',
  updated_at text not null,
  primary key(tenant_id, user_id, id)
);
create index if not exists conversations_last_message on conversations(last_message_at desc);
create index if not exists conversations_team on conversations(tenant_id, user_id, team_id);
create table if not exists messages(
  tenant_id text not null,
  user_id text not null,
  conversation_id text not null,
  id text not null,
  reply_chain_id text not null default '',
  parent_message_id text not null default '',
  client_message_id text not null default '',
  sender_id text not null default '',
  sender_name text not null default '',
  sent_at text not null,
  edited_at text,
  deleted_at text,
  message_type text not null default '',
  content_type text not null default '',
  content_html text not null default '',
  content_text text not null default '',
  version integer not null default 0,
  mentions_json text,
  mentions_me integer not null default 0,
  reactions_json text,
  files_json text,
  links_json text,
  subject text not null default '',
  importance text not null default '',
  pinned integer not null default 0,
  link text not null default '',
  raw_json text,
  content_hash text not null default '',
  updated_at text not null,
  primary key(tenant_id, user_id, conversation_id, id)
);
create index if not exists messages_sent_at on messages(sent_at desc);
create index if not exists messages_conversation_sent on messages(conversation_id, sent_at desc);
create index if not exists messages_sender_sent on messages(sender_id, sent_at desc);
create index if not exists messages_reply_chain on messages(tenant_id, user_id, conversation_id, reply_chain_id);
create index if not exists messages_parent on messages(tenant_id, user_id, conversation_id, parent_message_id);
create table if not exists people(
  tenant_id text not null,
  id text not null,
  display_name text not null default '',
  first_seen_at text,
  last_seen_at text,
  primary key(tenant_id, id)
);
create table if not exists activity(
  tenant_id text not null,
  user_id text not null,
  id text not null,
  type text not null default '',
  subtype text not null default '',
  is_read integer not null default 0,
  at text not null,
  conversation_id text not null default '',
  message_id text not null default '',
  reply_chain_id text not null default '',
  app_id text not null default '',
  raw_json text,
  content_hash text not null default '',
  updated_at text not null,
  primary key(tenant_id, user_id, id)
);
create index if not exists activity_at on activity(at desc);
create index if not exists activity_message on activity(tenant_id, user_id, conversation_id, message_id);
create table if not exists sync_runs(
  id integer primary key autoincrement,
  started_at text not null,
  finished_at text,
  source text not null default '',
  fingerprint text not null default '',
  status text not null,
  counts_json text,
  omissions_json text,
  accounts_json text
);
create table if not exists meta(
  key text primary key,
  value text not null
);
create table if not exists records(
  source text not null,
  tenant_id text not null default '',
  user_id text not null default '',
  database text not null,
  store text not null,
  key_json text not null,
  value_json text,
  content_hash text not null,
  first_seen_at text not null,
  updated_at text not null,
  removed_at text,
  raw_digest blob,
  value_redacted integer not null default 0,
  primary key(source, database, store, key_json)
);
create index if not exists records_db_store on records(database, store);
create index if not exists records_updated on records(updated_at);
create table if not exists typed_memo(
  source text not null,
  database text not null,
  key_json text not null,
  digest blob not null,
  effects blob not null,
  primary key(source, database, key_json)
);
create virtual table if not exists message_fts using fts5(message_key unindexed, content);
create virtual table if not exists conversation_fts using fts5(conversation_id unindexed, title);
`
