package store

const engageContentSchemaDDL = `
create table if not exists engage_locator_metadata(
 locator_id integer primary key,target_url text not null
);
create table if not exists engage_source_metadata(
 source_id integer primary key,account_json text not null
);
create table if not exists engage_sessions(
 rowid integer primary key,source_id integer not null,capture_id integer not null,
 representation_digest text not null,qualification text not null,result_json text not null,
 acquired_at text not null,captured_at text not null,unique(source_id,representation_digest)
);
create table if not exists engage_occurrences(
 rowid integer primary key,source_id integer not null,session_id integer not null,
 ordinal integer not null,native_thread_id text not null,native_starter_id text not null,
 starter_modified_at text,unique(session_id,ordinal)
);
create table if not exists engage_capture_members(
 capture_id integer primary key,session_id integer not null
);
create table if not exists engage_source_state(
 source_id integer primary key,current_session_id integer not null,last_good_session_id integer
);
create virtual table if not exists engage_fts using fts5(title,text,metadata);
`
