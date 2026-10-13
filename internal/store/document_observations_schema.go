package store

// Schema 14 adds the variant discriminator to the locator, observation and capture rows, and the
// registry payload and opened time to observations. An Office JSON row keeps variant office_json,
// its surface, application and document_json, and has no registry payload. An Office registry row
// has variant office_registry, an empty surface, the fixed source label registry, an empty
// document_json, the complete native observation in registry_json and a null modified_at.
// Schema 15 adds the OneDrive usage variant: variant onedrive_usage, an empty surface, the fixed
// source label onedrive, an empty document_json, the complete native record in usage_json, and the
// full native Type, ID and Format in native_key (empty for every other variant). A title or URL is
// stored only where the record states one. document_usage_relations holds, per capture, the edge
// from each recent row to every formatted variant with the same native ID.
// Existing tables gain the columns in Store.migrate; nothing below may index or reference them.
const documentObservationsSchemaDDL = `
create table if not exists document_locator_metadata(
 locator_id integer primary key,
 surface text not null,
 application text not null,
 variant text not null default 'office_json'
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
 variant text not null default 'office_json',
 registry_json text,
 opened_at text,
 usage_json text,
 native_key text not null default '',
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
 physical_observations integer not null,
 variant text not null default 'office_json'
);
create virtual table if not exists document_fts using fts5(title,metadata);
create table if not exists document_usage_relations(
 capture_id integer not null,
 recent_rowid integer not null,
 document_rowid integer not null,
 primary key(capture_id,recent_rowid,document_rowid)
);
create index if not exists document_usage_relations_document on document_usage_relations(capture_id,document_rowid);
`
