package store

// loopObservationsSchemaDDL holds the cached Loop channel projection (schema 13). A Loop source is a
// knowledge locator of kind loop_cache whose immutable identity is the exact archived record tuple in
// loop_locator_metadata. loop_attempt_evidence keeps each attempt's observed owner fields, the
// snapshot it admitted or reused and the record fingerprint it admitted, which later reads compare
// before loading a value. Snapshots are immutable per locator, value digest and reader
// qualification; channels and their FTS rows belong to exactly one snapshot.
const loopObservationsSchemaDDL = `
create table if not exists loop_locator_metadata(
 locator_id integer primary key,
 source text not null,
 database text not null,
 store text not null,
 key_json text not null,
 unique(source,database,store,key_json)
);
create table if not exists loop_attempt_evidence(
 attempt_id integer primary key,
 locator_id integer not null,
 tenant_id text not null,
 user_id text not null,
 value_digest text,
 snapshot_id integer,
 record_content_hash text,
 record_value_length integer,
 record_updated_at text,
 record_raw_digest blob,
 record_value_redacted integer
);
create table if not exists loop_snapshots(
 id integer primary key,
 locator_id integer not null,
 knowledge_source_id integer not null,
 value_digest text not null,
 qualification text not null,
 file_id text not null,
 sequence integer not null,
 latest_sequence integer not null,
 pending_operations integer not null,
 missing_blobs integer not null,
 partial integer not null,
 channel_count integer not null,
 tenant_id text not null,
 user_id text not null,
 record_updated_at_at_admission text not null,
 projected_at text not null,
 first_attempt integer not null,
 unique(locator_id,value_digest,qualification)
);
create table if not exists loop_snapshot_losses(
 snapshot_id integer not null,
 code text not null,
 count integer not null,
 primary key(snapshot_id,code)
);
create table if not exists loop_channels(
 id integer primary key,
 snapshot_id integer not null,
 ordinal integer not null,
 path text not null,
 sequence integer not null,
 text text not null,
 markers integer not null,
 pending_operations integer not null,
 unique(snapshot_id,ordinal)
);
create virtual table if not exists loop_fts using fts5(path,text);
`
