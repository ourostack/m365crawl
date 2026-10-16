package store

import (
	"slices"
	"strings"
)

const attendeeTablesDDL = `
create table if not exists outlook_calendar_reads(
 rowid integer primary key, account_id text not null, origin text,
 fingerprint text not null, mapper_stamp text not null, read_at text not null,
 receipt_digest text not null, unique(account_id,rowid)
);
create index if not exists outlook_calendar_reads_account on outlook_calendar_reads(account_id,rowid);
create table if not exists outlook_calendar_read_state(
 account_id text primary key, read_id integer not null,
 foreign key(account_id,read_id) references outlook_calendar_reads(account_id,rowid)
);
create table if not exists outlook_link_events(
 rowid integer primary key autoincrement, account_id text not null,
 unique(account_id,rowid)
);
create index if not exists outlook_link_events_account on outlook_link_events(account_id,rowid);
create table if not exists outlook_link_state(
 account_id text primary key, event_id integer,
 foreign key(account_id,event_id) references outlook_link_events(account_id,rowid)
);
create table if not exists outlook_attendee_sources(
 source_id integer primary key references knowledge_sources(id),
 calendar_account text not null, origin text not null
);
create index if not exists outlook_attendee_sources_account on outlook_attendee_sources(calendar_account,origin);
create table if not exists outlook_attendee_requests(
 attempt_id integer primary key references knowledge_attempts(id), request_json text not null
);
create table if not exists outlook_attendee_snapshots(
 rowid integer primary key,
 source_id integer not null references outlook_attendee_sources(source_id),
 first_attempt_id integer not null references knowledge_attempts(id),
 representation_digest text not null, qualification text not null, result_json text not null,
 acquired_at text not null, source_observed_at text,
 unique(source_id,representation_digest), unique(source_id,rowid)
);
create table if not exists outlook_attendee_occurrences(
 rowid integer primary key,
 source_id integer not null, snapshot_id integer not null, ordinal integer not null,
 native_key integer not null, detail_key integer not null,
 unique(snapshot_id,ordinal),
 foreign key(source_id,snapshot_id) references outlook_attendee_snapshots(source_id,rowid)
);
create index if not exists outlook_attendee_occurrences_parent on outlook_attendee_occurrences(source_id,snapshot_id,detail_key,ordinal);
create table if not exists outlook_attendee_bindings(
 rowid integer primary key, source_id integer not null, snapshot_id integer not null,
 ordinal integer not null, native_event_id text not null, detail_key integer not null,
 unique(snapshot_id,ordinal),
 foreign key(source_id,snapshot_id) references outlook_attendee_snapshots(source_id,rowid)
);
create index if not exists outlook_attendee_bindings_event on outlook_attendee_bindings(source_id,native_event_id,snapshot_id,ordinal);
create table if not exists outlook_attendee_members(
 attempt_id integer primary key references knowledge_attempts(id),
 snapshot_id integer not null references outlook_attendee_snapshots(rowid),
 calendar_read_id integer not null references outlook_calendar_reads(rowid),
 link_revision integer not null,
 owner_link_json text not null, census_json text not null, input_digest text not null,
 acquired_at text not null, source_observed_at text, admitted_at text not null,
 membership_digest text not null
);
create index if not exists outlook_attendee_members_read on outlook_attendee_members(calendar_read_id,attempt_id);
create index if not exists outlook_attendee_members_snapshot on outlook_attendee_members(snapshot_id,attempt_id);
`

const knownOutlookAccountsSQL = `
select account_id from calendar_sources where source='outlook'
union select account_id from calendar_source_events where source='outlook'
union select account_id from calendar_account_links where source='outlook'
union select substr(key,14) from meta where key like 'outlook_read:%'
union select account_id from outlook_calendar_reads
union select calendar_account from outlook_attendee_sources
union select account_id from outlook_link_events`

var attendeeTriggerSQL = buildAttendeeTriggers()

func buildAttendeeTriggers() map[string]string {
	guard := func(account string, allowNew bool) string {
		var bootstrap string
		if allowNew {
			bootstrap = `select case when not exists(select 1 from outlook_link_state where account_id=` + account + `)
and exists(select 1 from (` + knownOutlookAccountsSQL + `) where account_id=` + account + `)
then raise(abort,'outlook_attendee_archive_corrupt') end;
insert into outlook_link_state(account_id,event_id) values(` + account + `,null) on conflict(account_id) do nothing;
`
		}
		return bootstrap + `select case when
not exists(select 1 from outlook_link_state where account_id=` + account + `)
or (select event_id from outlook_link_state where account_id=` + account + `) is not
   (select max(rowid) from outlook_link_events where account_id=` + account + `)
or exists(select 1 from outlook_link_state s where s.account_id=` + account + `
 and s.event_id is not null and not exists(select 1 from outlook_link_events e where e.rowid=s.event_id and e.account_id=s.account_id))
then raise(abort,'outlook_attendee_archive_corrupt') end;
`
	}
	advance := func(account string) string {
		return `insert into outlook_link_events(account_id) values(` + account + `);
update outlook_link_state set event_id=last_insert_rowid() where account_id=` + account + `;`
	}
	changed := `(old.source is not new.source or old.account_id is not new.account_id or old.principal_id is not new.principal_id
or old.method is not new.method or old.evidence is not new.evidence or old.linked_at is not new.linked_at or old.unlinked_at is not new.unlinked_at)`
	return map[string]string{
		"outlook_attendee_link_insert": `CREATE TRIGGER IF NOT EXISTS outlook_attendee_link_insert BEFORE INSERT ON calendar_account_links
WHEN new.source='outlook' BEGIN ` + guard("new.account_id", true) + ` END`,
		"outlook_attendee_link_insert_event": `CREATE TRIGGER IF NOT EXISTS outlook_attendee_link_insert_event AFTER INSERT ON calendar_account_links
WHEN new.source='outlook' BEGIN ` + advance("new.account_id") + ` END`,
		"outlook_attendee_link_update_old": `CREATE TRIGGER IF NOT EXISTS outlook_attendee_link_update_old BEFORE UPDATE ON calendar_account_links
WHEN old.source='outlook' and ` + changed + ` BEGIN ` + guard("old.account_id", false) + advance("old.account_id") + ` END`,
		"outlook_attendee_link_update_new": `CREATE TRIGGER IF NOT EXISTS outlook_attendee_link_update_new BEFORE UPDATE ON calendar_account_links
WHEN new.source='outlook' and (old.source is not new.source or old.account_id is not new.account_id)
BEGIN ` + guard("new.account_id", true) + advance("new.account_id") + ` END`,
		"outlook_attendee_link_delete": `CREATE TRIGGER IF NOT EXISTS outlook_attendee_link_delete BEFORE DELETE ON calendar_account_links
WHEN old.source='outlook' BEGIN ` + guard("old.account_id", false) + advance("old.account_id") + ` END`,
		"outlook_attendee_source_insert": `CREATE TRIGGER IF NOT EXISTS outlook_attendee_source_insert BEFORE INSERT ON calendar_sources
WHEN new.source='outlook' BEGIN ` + guard("new.account_id", true) + ` END`,
		"outlook_attendee_event_insert": `CREATE TRIGGER IF NOT EXISTS outlook_attendee_event_insert BEFORE INSERT ON calendar_source_events
WHEN new.source='outlook' BEGIN ` + guard("new.account_id", true) + ` END`,
	}
}

func attendeeSchemaDDL() string {
	var b strings.Builder
	b.WriteString(attendeeTablesDDL)
	b.WriteString(`insert into outlook_link_state(account_id,event_id) select account_id,null from (`)
	b.WriteString(knownOutlookAccountsSQL)
	b.WriteString(`) where true on conflict(account_id) do nothing;`)
	names := make([]string, 0, len(attendeeTriggerSQL))
	for name := range attendeeTriggerSQL {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		b.WriteString(attendeeTriggerSQL[name])
		b.WriteByte(';')
	}
	return b.String()
}
