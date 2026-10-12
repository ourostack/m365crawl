package store

const documentObservationsSchemaDDL = `
create table if not exists document_locator_metadata(
 locator_id integer primary key,
 surface text not null,
 application text not null
);
create table if not exists document_observations(
 rowid integer primary key,
 source_id integer not null,
 capture_id integer not null,
 representation_digest text not null,
 ordinal integer not null,
 surface text not null,
 application text not null,
 title text not null,
 url text not null,
 web_url text not null,
 modified_at text,
 document_json text not null,
 unique(source_id,representation_digest)
);
create index if not exists document_observations_source on document_observations(source_id,modified_at);
create index if not exists document_observations_url on document_observations(url);
create index if not exists document_observations_web_url on document_observations(web_url);
create table if not exists document_capture_members(
 capture_id integer not null,
 observation_rowid integer not null,
 primary key(capture_id,observation_rowid)
);
create table if not exists document_capture_metadata(
 capture_id integer primary key,
 surface text not null,
 application text not null,
 acquired_at text,
 source_observed_at text,
 reused integer not null,
 physical_observations integer not null
);
create virtual table if not exists document_fts using fts5(title,metadata);
`
