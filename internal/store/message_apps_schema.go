package store

import "strconv"

// MessageApplicationVersion numbers how teamsdesktop.ProjectApplication turns a retained Teams
// message into application, reply and citation evidence. Raise it when the projection changes what
// it admits: the next writable sync then projects every retained message once more under the new
// version (BackfillMessageApplications), and evidence of the older version stays readable only as
// an explicitly retained fallback. Version 2 decodes a JSON-string properties.botMetadata, the
// container's native encoding, as the object it holds.
const MessageApplicationVersion = 2

// appMarkerSettled reports whether the backfill marker stored in meta records a complete backfill
// at or above the active version. An older binary on a newer archive is settled, so it neither
// reprocesses nor rewrites the marker; an unreadable marker is not.
func appMarkerSettled(marker string) bool {
	v, err := strconv.Atoi(marker)
	return err == nil && v >= activeApplicationVersion
}

// activeApplicationVersion is MessageApplicationVersion; tests raise it to exercise a version change.
var activeApplicationVersion = MessageApplicationVersion

// messageAppKey is the meta key that records the mapper version of the last complete backfill.
const messageAppKey = "message_app_mapper_version"

// messageApplicationsSchemaDDL holds the immutable application-evidence representations and the
// per-message current and last-good pointers. A representation is unique by the complete message
// key, the source sender, whether raw JSON was present, the SHA256 of the exact retained raw bytes
// (empty when raw is absent) and the mapper version; it is never updated or deleted. rowid is the
// public observation id (message-app-observation:N), which `sql` can read back.
const messageApplicationsSchemaDDL = `
create table if not exists message_app_observations(
 rowid integer primary key,
 tenant_id text not null,
 user_id text not null,
 conversation_id text not null,
 message_id text not null,
 sender_id text not null,
 raw_present integer not null check(raw_present in (0,1)),
 raw_digest text not null,
 mapper_version integer not null check(mapper_version>0),
 state text not null check(state in ('mapped','partial','failed','absent')),
 failure_code text not null,
 projection_json text not null,
 derived_at text not null,
 unique(tenant_id,user_id,conversation_id,message_id,sender_id,raw_present,raw_digest,mapper_version)
);
create table if not exists message_app_current(
 tenant_id text not null,
 user_id text not null,
 conversation_id text not null,
 message_id text not null,
 current_id integer not null references message_app_observations(rowid),
 last_good_id integer references message_app_observations(rowid),
 primary key(tenant_id,user_id,conversation_id,message_id)
);
`

// ActiveApplicationVersion is the mapper version this build projects and reads evidence under.
func ActiveApplicationVersion() int { return activeApplicationVersion }
