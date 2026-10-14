package store

const localTextSchemaDDL = `
create table if not exists local_text_locator_metadata(
 locator_id integer primary key,path text not null,requested_kind text not null
);
create table if not exists local_file_snapshots(
 rowid integer primary key,source_id integer not null,capture_id integer not null,
 source_byte_digest text not null,evidence_json text not null,qualification text not null,
 acquired_at text not null,captured_at text not null,
 unique(source_id,source_byte_digest,evidence_json,qualification)
);
create table if not exists local_text_observations(
 rowid integer primary key,source_id integer not null,capture_id integer not null,
 snapshot_id integer not null,representation_digest text not null,requested_kind text not null,
 verified_kind text,result_json text not null,file_modified_at text,
 unique(source_id,representation_digest)
);
create table if not exists local_text_capture_members(
 capture_id integer not null,observation_rowid integer not null,
 primary key(capture_id,observation_rowid)
);
create table if not exists local_text_source_state(
 source_id integer primary key,current_snapshot_id integer not null,last_good_observation_id integer
);
create table if not exists local_text_attempt_metadata(
 attempt_id integer primary key,request_origin text not null
);
create table if not exists local_text_references(
 attempt_id integer not null,capture_id integer not null,observation_rowid integer not null,
 document_observation_id integer not null,original_url text not null,association_state text not null,
 primary key(attempt_id,document_observation_id,original_url)
);
create virtual table if not exists local_text_fts using fts5(label,text,metadata);
`
