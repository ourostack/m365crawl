package store

// SchemaVersion is the archive schema version recorded through crawlkit's schema_migrations.
const SchemaVersion = 1

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
  read_horizon_message_id text not null default '',
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
  omissions_json text
);
create virtual table if not exists message_fts using fts5(message_key unindexed, content);
create virtual table if not exists conversation_fts using fts5(conversation_id unindexed, title);
`
