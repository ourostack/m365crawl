package store

// mailSchemaDDL is the mail archive (schema version 6). Times are UTC text in timeLayout, like the
// rest of the archive. A message is identified by (account, detail_key); the unique key lets a
// re-keyed row (see CommitMail) keep its rowid, which mail_fts, mail_recipients and mail_attachments
// all use. subject_norm compares without regard to ASCII case, so the thread fallback and its
// index need no lower().
//
// body_state is what the read found: inline, file (read), pending (a body file not read yet),
// missing, unreadable or none. mail_absent holds the first trusted read that missed a message; a
// second trusted read of a different store copy confirms the absence (see CommitMail).
const mailSchemaDDL = `
create table if not exists mail_folders(
  account text not null,
  folder_key integer not null,
  parent_key integer not null default 0,
  name text not null default '',
  kind text not null default 'other',
  primary key(account, folder_key)
);
create table if not exists mail_messages(
  account text not null,
  detail_key integer not null,
  internet_message_id text not null default '',
  folder_key integer not null,
  to_me integer not null default 0,
  subject text not null default '',
  subject_norm text not null default '' collate nocase,
  sender_name text not null default '',
  sender_address text not null default '',
  preview text not null default '',
  in_reply_to text not null default '',
  received_at text,
  sent_at text,
  class text not null default '',
  importance text not null default '',
  ical_uid text not null default '',
  is_read integer,
  read_state text not null default 'unknown',
  flag text not null default 'unknown',
  has_attachments integer not null default 0,
  body_state text not null default 'none',
  body_html_gz blob,
  body_text text not null default '',
  body_text_version integer not null default 0,
  first_seen_at text not null,
  state_seen_at text not null,
  gone_at text,
  evicted_at text,
  unique(account, detail_key)
);
create index if not exists mail_messages_received on mail_messages(received_at desc);
create index if not exists mail_messages_folder on mail_messages(account, folder_key, received_at desc);
create index if not exists mail_messages_message_id on mail_messages(account, internet_message_id);
create index if not exists mail_messages_in_reply_to on mail_messages(account, in_reply_to);
create index if not exists mail_messages_subject_norm on mail_messages(account, subject_norm);
create index if not exists mail_messages_ical_uid on mail_messages(ical_uid);
create table if not exists mail_recipients(
  message_rowid integer not null,
  ord integer not null,
  name text not null default '',
  address text,
  kind_raw integer not null default 0,
  primary key(message_rowid, ord)
);
create table if not exists mail_attachments(
  message_rowid integer not null,
  attachment_key integer not null,
  name text not null default '',
  size integer not null default 0,
  content_type text not null default '',
  inline integer not null default 0,
  downloaded integer not null default 0,
  primary key(message_rowid, attachment_key)
);
create table if not exists mail_coverage(
  account text not null,
  folder_key integer not null,
  oldest_at text,
  newest_at text,
  count integer not null default 0,
  read_at text not null,
  primary key(account, folder_key)
);
create table if not exists mail_absent(
  account text not null,
  detail_key integer not null,
  first_missed_at text not null,
  misses integer not null default 1,
  fresh_at text not null default '',
  primary key(account, detail_key)
);
create virtual table if not exists mail_fts using fts5(subject, sender_name, sender_address, body_text);
`
