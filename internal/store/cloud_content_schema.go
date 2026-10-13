package store

const cloudContentSchemaDDL = `
create table if not exists cloud_locator_metadata(
 locator_id integer primary key,
 request_json text not null
);
create table if not exists cloud_observations(
 rowid integer primary key,
 source_id integer not null,
 capture_id integer not null,
 representation_digest text not null,
 result_json text not null,
 kind text not null,
 title text,
 file_modified_at text,
 unique(source_id,representation_digest)
);
create table if not exists cloud_capture_members(
 capture_id integer not null,
 observation_rowid integer not null,
 primary key(capture_id,observation_rowid)
);
create table if not exists cloud_capture_metadata(
 capture_id integer primary key,
 requested_transcript_id text,
 current_transcript_id text,
 body_observation_id integer,
 completed_at text not null
);
create table if not exists cloud_source_body_state(
 source_id integer primary key,
 last_good_observation_id integer,
 current_transcript_id text
);
create virtual table if not exists cloud_fts using fts5(title,text,metadata);
`
