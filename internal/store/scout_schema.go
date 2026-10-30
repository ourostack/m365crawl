package store

// scoutSchemaDDL holds the optional Scout session history (schema 21). A source is one app folder
// that `scout sync` read. A file row is one session file name inside one device folder of it; its
// state is what the last listing saw, and gone_at marks a file a complete listing no longer had
// (the archive keeps everything it read). An observation is one immutable read of a file's
// distinct bytes under one parser version; its messages and their FTS rows belong to it alone.
// scout_messages has an explicit integer primary key, so the FTS rowid link survives VACUUM.
const scoutSchemaDDL = `
create table if not exists scout_sources(
 id integer primary key,
 platform text not null,
 root text not null,
 registered_at text not null,
 last_run integer,
 unique(platform,root)
);
create table if not exists scout_runs(
 id integer primary key autoincrement,
 source_id integer not null,
 started_at text not null,
 finished_at text,
 outcome text not null default 'pending',
 reason text not null default '',
 devices integer not null default 0,
 unreadable_devices integer not null default 0,
 listed integer not null default 0,
 admitted integer not null default 0,
 unchanged integer not null default 0,
 cloud_only integer not null default 0,
 failed integer not null default 0,
 gone integer not null default 0
);
create table if not exists scout_files(
 id integer primary key,
 source_id integer not null,
 device_id text not null,
 file_name text not null,
 state text not null,
 reason text not null default '',
 last_run integer not null,
 listed_at text not null,
 gone_at text,
 read_size integer,
 read_modified_ns integer,
 latest_observation integer,
 unique(source_id,device_id,file_name)
);
create table if not exists scout_observations(
 id integer primary key,
 file_id integer not null,
 digest text not null,
 parser_version integer not null,
 size integer not null,
 file_modified_at text not null,
 captured_at text not null,
 run_id integer not null,
 native_id text not null,
 name text not null,
 model text not null,
 origin text not null,
 kind text not null,
 device_claim text not null,
 automation integer not null,
 created_raw text not null,
 created_at text,
 updated_raw text not null,
 updated_at text,
 message_count integer not null,
 first_message_at text,
 last_message_at text,
 redactions integer not null,
 losses_json text not null,
 raw_json text not null,
 unique(file_id,digest,parser_version)
);
create table if not exists scout_messages(
 id integer primary key,
 observation_id integer not null,
 ordinal integer not null,
 native_id text not null,
 role text not null,
 at_raw text not null,
 at text,
 text text not null,
 tool_calls integer not null,
 file_names integer not null,
 reasoning integer not null,
 digest text not null,
 unique(observation_id,ordinal)
);
create index if not exists scout_messages_at on scout_messages(at);
create virtual table if not exists scout_fts using fts5(name,text);
`
