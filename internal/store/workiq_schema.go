package store

// WorkIQ state is append/update-only, including retired pages and cursors.
const workiqSchemaDDL = `
create table if not exists workiq_accounts(
 family text not null check(family in ('mail','chats')), account_id text not null,
 user_id text not null, tenant_id text not null default '', address text not null,
 binding text not null check(binding in ('stored_identity','explicit_bootstrap')),
 bound_at text not null, primary key(family,account_id)
);
create table if not exists workiq_attempts(
 id integer primary key autoincrement,
 family text not null check(family in ('mail','chats')), account_id text not null,
 started_at text not null, finished_at text,
 status text not null default 'open' check(status in ('open','ok','ok_with_omissions','partial','failed')),
 window_start text, qualification text not null default 'workiq-v1',
 error_code text not null default '', counts_json text not null default '{}',
 gap_count integer not null default 0 check(gap_count>=0),
 all_folders integer not null default 0 check(all_folders in (0,1))
);
create index if not exists workiq_attempts_account on workiq_attempts(family,account_id,id);
create table if not exists workiq_frontiers(
 id integer primary key autoincrement,
 family text not null, account_id text not null, resource text not null,
 scope text not null default '', qualification text not null,
 cursor text not null default '', pending_cursor text not null default '', revision integer not null default 0,
 baseline integer not null default 1, retired integer not null default 0,
 unique(family,account_id,resource,scope,qualification)
);
create table if not exists workiq_pages(
 id integer primary key autoincrement, attempt_id integer not null references workiq_attempts(id),
 frontier_id integer not null references workiq_frontiers(id), expected_revision integer not null,
 data_json text not null, next_cursor text not null default '', delta_cursor text not null default '',
 observed_at text not null,
 state text not null default 'staged' check(state in ('staged','admitted','retired')),
 unique(attempt_id,frontier_id,expected_revision)
);
create table if not exists workiq_rows(
 kind text not null check(kind in ('mail','message','conversation')),
 account_id text not null, item_rowid integer not null,
 graph_id text not null, server_json text not null, server_projection text not null default '{}',
 cache_projection text, cache_first integer not null check(cache_first in (0,1)),
 cache_seen integer not null check(cache_seen in (0,1)), server_seen integer not null default 1,
 cache_seen_at text, server_seen_at text not null,
 winner text not null check(winner in ('desktop_cache','workiq_server')),
 cache_version integer, server_version text,
 cache_deleted_at text, server_deleted_at text, cache_first_miss_at text,
 cache_gone_at text, cache_evicted_at text, server_gone_at text,
 server_first_missing_census integer, modified_at text not null,
 primary key(kind,account_id,item_rowid)
);
create index if not exists workiq_rows_graph on workiq_rows(kind,account_id,graph_id);
create table if not exists workiq_aliases(
 kind text not null, account_id text not null, namespace text not null,
 alias text not null, item_rowid integer not null,
 primary key(kind,account_id,namespace,alias)
);
create table if not exists workiq_source_keys(
 account text not null,
 kind text not null check(kind in ('message','folder','attachment')),
 source_id text not null,
 key integer not null check(typeof(key)='integer' and key between 1 and 4294967295),
 primary key(account,kind,source_id), unique(account,kind,key)
);
create table if not exists workiq_folders(
 account_id text not null, graph_id text not null, folder_key integer not null,
 parent_graph_id text not null default '', well_known text not null default 'other',
 inventory_attempt integer not null, total_items integer, resource_path text not null default '',
 primary key(account_id,graph_id)
);
create table if not exists workiq_censuses(
 id integer primary key autoincrement, attempt_id integer not null references workiq_attempts(id),
 account_id text not null, completed_at text, complete integer not null default 0,
 unique(attempt_id,account_id)
);
create table if not exists workiq_census_members(
 census_id integer not null references workiq_censuses(id), normalized_id text not null,
 graph_id text not null, folder_id text not null,
 sender text not null, sent_at text, subject_norm text not null,
 primary key(census_id,graph_id)
);
create table if not exists workiq_gaps(
 attempt_id integer not null references workiq_attempts(id), code text not null,
 resource text not null default '', n integer not null check(n>0),
 resolved integer not null default 0 check(resolved in (0,1)),
 primary key(attempt_id,code,resource)
);
create table if not exists workiq_mail_receipts(
 id integer primary key autoincrement, attempt_id integer not null unique references workiq_attempts(id),
 account_id text not null, completed_at text not null, folders integer not null check(folders>=0)
);
`
