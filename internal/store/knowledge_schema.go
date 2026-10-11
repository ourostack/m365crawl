package store

const knowledgeSchemaDDL = `
create table if not exists knowledge_locators(
 id integer primary key,
 kind text not null,
 platform text not null,
 locator text not null,
 latest_attempt integer,
 last_admitted integer,
 unique(kind,platform,locator)
);
create table if not exists knowledge_attempts(
 id integer primary key autoincrement,
 locator_id integer not null,
 source_id integer,
 started_at text not null,
 finished_at text,
 outcome text not null default 'pending',
 acquisition_signature text,
 content_digest text,
 reader_version integer not null default 1,
 mapper_version integer not null default 1,
 coverage text not null default 'unknown',
 losses_json text not null default '{}',
 reason text not null default ''
);
create index if not exists knowledge_attempts_locator on knowledge_attempts(locator_id,id);
create table if not exists knowledge_sources(
 id integer primary key,
 locator_id integer not null,
 boundary text not null,
 qualification text not null,
 admitted_attempt integer,
 complete_attempt integer,
 source_observed_at text,
 unique(locator_id,boundary,qualification)
);
create table if not exists note_entities(
 rowid integer primary key,
 source_id integer not null,
 goid text not null,
 guid text not null,
 gosid text not null,
 parent_goid text not null,
 grandparent_goids text not null,
 type integer not null,
 title text not null,
 modified_at text,
 capture_id integer not null,
 unique(source_id,goid)
);
create index if not exists note_entities_source on note_entities(source_id,type,modified_at);
create table if not exists note_elements(
 entity_rowid integer not null,
 native_entity_rowid integer not null,
 native_rowid integer not null,
 goid text not null,
 text text not null,
 kind text not null,
 jcid integer not null,
 hashtag_markers integer not null,
 capture_id integer not null,
 primary key(entity_rowid,native_rowid)
);
create table if not exists note_flags(
 entity_rowid integer not null,
 native_rowid integer not null,
 element_native_rowid integer not null,
 type integer not null,
 shape integer not null,
 status integer not null,
 label text not null,
 capture_id integer not null,
 primary key(entity_rowid,native_rowid)
);
create virtual table if not exists note_fts using fts5(title,text);
`
