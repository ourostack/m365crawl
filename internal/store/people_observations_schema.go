package store

const peopleObservationsSchemaDDL = `
create table if not exists people_observations(
 rowid integer primary key,
 source_id integer not null,
 capture_id integer not null,
 observation_digest text not null,
 native_class integer not null,
 native_key integer not null,
 native_parent integer not null,
 version_raw text not null,
 resynced integer not null,
 display_name text not null,
 first_name text not null,
 last_name text not null,
 email text not null,
 alternate_email text not null,
 teams_id text not null,
 refreshed_at text,
 unique(source_id,observation_digest)
);
create index if not exists people_observations_group on people_observations(source_id,native_class,native_key);
create index if not exists people_observations_email on people_observations(source_id,email);
create table if not exists people_capture_members(
 capture_id integer not null,
 observation_rowid integer not null,
 primary key(capture_id,observation_rowid)
);
create table if not exists people_group_ambiguities(
 source_id integer not null,
 native_class integer not null,
 native_key integer not null,
 first_capture_id integer not null,
 primary key(source_id,native_class,native_key)
);
create table if not exists people_capture_metadata(
 capture_id integer primary key,
 acquired_at text,
 source_observed_at text,
 reused integer not null,
 physical_observations integer not null
);
create table if not exists people_capture_links(
 capture_id integer primary key,
 profile_account text not null,
 principal text not null,
 method text not null,
 linked_at text
);
`
