//go:build acceptance

package acceptance

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

// Private values stay inside this comparator. Its only output is a sorted fixed field-name set.
func owaCompare(st *store.Store, account string, c owaRawCensus) []string {
	ctx, cancel := context.WithTimeout(context.Background(), owaWorkTimeout)
	defer cancel()
	return owaCompareContext(ctx, st, account, c)
}

func owaCompareContext(ctx context.Context, st *store.Store, account string, c owaRawCensus, states ...*owaFailureState) []string {
	bad := map[string]bool{}
	query := func(q string) [][]any {
		rows, err := owaQueryContext(ctx, st, q, states...)
		if err != nil {
			bad["archive_query"] = true
		}
		return rows
	}
	ids := map[string]map[string]int64{"message": {}, "folder": {}, "attachment": {}}
	reverse := map[string]map[int64]string{"message": {}, "folder": {}, "attachment": {}}
	wantIDs := map[string]map[string]bool{"message": {}, "folder": {}, "attachment": {}}
	for id, f := range c.folders {
		wantIDs["folder"][id], wantIDs["folder"][f.parent] = true, true
	}
	for id, m := range c.messages {
		wantIDs["message"][id] = true
		for id := range m.attachments {
			wantIDs["attachment"][id] = true
		}
	}
	for _, row := range query(`select account,kind,source_id,key from mail_source_ids`) {
		if row[0] != account {
			continue
		}
		kind, id := asString(row[1]), asString(row[2])
		key, ok := row[3].(int64)
		if ids[kind] == nil || !ok || key <= 0 || key > 4294967295 || !wantIDs[kind][id] {
			bad["source_id"] = true
			continue
		}
		if _, duplicate := reverse[kind][key]; duplicate {
			bad["assigned_key"] = true
		}
		ids[kind][id], reverse[kind][key] = key, id
	}
	for kind, wanted := range wantIDs {
		if len(ids[kind]) != len(wanted) {
			bad["source_id"] = true
		}
	}
	root, rootErr := c.rootContext(ctx)
	rootFound := false
	for _, row := range query(`select key,value from meta where key like 'outlook_mail_root:%'`) {
		if row[0] == "outlook_mail_root:"+account {
			rootFound = true
			key, err := strconv.ParseInt(asString(row[1]), 10, 64)
			if err != nil || rootErr != nil || key != ids["folder"][root] {
				bad["folder_root"] = true
			}
		}
	}
	if !rootFound {
		bad["folder_root"] = true
	}
	folderCount := 0
	for _, row := range query(`select account,folder_key,parent_key,name,kind from mail_folders`) {
		if row[0] != account {
			continue
		}
		folderCount++
		id := reverse["folder"][owaNumber(row[1])]
		want, found := c.folders[id]
		if !found || !reflect.DeepEqual(row[2:], []any{ids["folder"][want.parent], want.name, want.kind}) {
			bad["folder_parent"] = true
		}
	}
	if folderCount != len(c.folders) {
		bad["folders"] = true
	}
	messageRows := map[int64]owaRawMessage{}
	messages := query(`select account,rowid,detail_key,folder_key,internet_message_id,conversation_id,to_me,subject,sender_name,sender_address,preview,in_reply_to,received_at,sent_at,class,importance,ical_uid,is_read,read_state,flag,has_attachments,body_state,body_html_gz,body_text,body_text_version,gone_at,evicted_at from mail_messages`)
	for _, row := range messages {
		if row[0] != account {
			continue
		}
		id := reverse["message"][owaNumber(row[2])]
		want, found := c.messages[id]
		if !found {
			bad["messages"] = true
			continue
		}
		key := owaNumber(row[1])
		messageRows[key] = want
		if row[3] != ids["folder"][want.folder] {
			bad["folder_key"] = true
		}
		for i, name := range owaMessageFields {
			matches := reflect.DeepEqual(row[i+4], want.fields[i])
			if name == "received_at" || name == "sent_at" {
				matches = owaTimeMatches(row[i+4], want.fields[i])
			}
			if !matches {
				bad[name] = true
			}
		}
		if !owaHTMLMatches(row[22], want.html) {
			bad["body_html_gz"] = true
		}
		if row[24] != int64(store.MailTextVersion) {
			bad["body_text_version"] = true
		}
		if !want.textKnown {
			bad["body_derivation_unproved"] = true
		} else if row[23] != want.text {
			bad["body_text"] = true
		}
		if row[25] != nil || row[26] != nil {
			bad["gone_at"] = true
		}
	}
	// Detect both missing and extra messages, even if a corrupt source-id mapping masks one.
	archCount := 0
	for _, row := range messages {
		if row[0] == account {
			archCount++
		}
	}
	if archCount != len(c.messages) || len(messageRows) != len(c.messages) {
		bad["messages"] = true
	}
	recipients := map[int64][][]any{}
	for _, row := range query(`select m.account,r.message_rowid,r.ord,r.name,r.address,r.kind_raw,r.role from mail_recipients r join mail_messages m on m.rowid=r.message_rowid order by r.message_rowid,r.ord`) {
		if row[0] == account {
			key := owaNumber(row[1])
			recipients[key] = append(recipients[key], row[2:])
		}
	}
	attachments := map[int64]map[string][]any{}
	for _, row := range query(`select m.account,a.message_rowid,a.attachment_key,a.name,a.size,a.content_type,a.inline,a.downloaded from mail_attachments a join mail_messages m on m.rowid=a.message_rowid`) {
		if row[0] != account {
			continue
		}
		key := owaNumber(row[1])
		if attachments[key] == nil {
			attachments[key] = map[string][]any{}
		}
		id := reverse["attachment"][owaNumber(row[2])]
		if id == "" {
			bad["attachment_key"] = true
		}
		attachments[key][id] = row[3:]
	}
	for rowid, want := range messageRows {
		if !reflect.DeepEqual(recipients[rowid], want.recipients) {
			bad["recipient_order_role_kind"] = true
		}
		if len(attachments[rowid]) != len(want.attachments) {
			bad["attachments"] = true
		}
		for id, fields := range want.attachments {
			if !reflect.DeepEqual(attachments[rowid][id], fields) {
				bad["attachment_metadata"] = true
			}
		}
	}
	ftsCount := 0
	for _, row := range query(`select m.account,m.rowid,f.subject,f.sender_name,f.sender_address,f.body_text,m.body_text from mail_messages m join mail_fts f on f.rowid=m.rowid`) {
		if row[0] != account {
			continue
		}
		ftsCount++
		want, found := messageRows[owaNumber(row[1])]
		if !found || !want.textKnown || !reflect.DeepEqual(row[2:5], want.fields[3:6]) || row[5] != row[6] || row[5] != want.text {
			bad["fts"] = true
		}
	}
	if ftsCount != len(c.messages) {
		bad["fts"] = true
	}
	// Exact per-message conversation equality above also proves account-scoped group membership.
	wantGroups, gotGroups := map[string]int{}, map[string]int{}
	for _, m := range c.messages {
		if id := asString(m.fields[1]); id != "" {
			wantGroups[id]++
		}
	}
	for _, row := range messages {
		if row[0] == account && row[5] != "" {
			gotGroups[asString(row[5])]++
		}
	}
	if !reflect.DeepEqual(wantGroups, gotGroups) {
		bad["conversation_groups"] = true
	}
	type coverage struct {
		count          int64
		oldest, newest time.Time
	}
	wantedCoverage := map[int64]coverage{}
	for _, m := range c.messages {
		key := ids["folder"][m.folder]
		v := wantedCoverage[key]
		v.count++
		if received := m.fields[8]; received != nil {
			at, ok := parseTime(asString(received))
			if !ok {
				bad["coverage_time"] = true
			}
			if v.oldest.IsZero() || at.Before(v.oldest) {
				v.oldest = at
			}
			if v.newest.IsZero() || at.After(v.newest) {
				v.newest = at
			}
		}
		wantedCoverage[key] = v
	}
	coverageCount := 0
	for _, row := range query(`select account,folder_key,count,oldest_at,newest_at from mail_coverage`) {
		if row[0] != account {
			continue
		}
		coverageCount++
		v, exists := wantedCoverage[owaNumber(row[1])]
		var oldest, newest any
		if !v.oldest.IsZero() {
			oldest, newest = v.oldest.Format(time.RFC3339Nano), v.newest.Format(time.RFC3339Nano)
		}
		if !exists || row[2] != v.count || !owaTimeMatches(row[3], oldest) || !owaTimeMatches(row[4], newest) {
			bad["mail_coverage"] = true
		}
	}
	if coverageCount != len(wantedCoverage) {
		bad["mail_coverage"] = true
	}
	for _, row := range query(`select account,detail_key from mail_absent`) {
		if row[0] == account {
			bad["mail_absent"] = true
		}
	}
	for _, check := range []struct{ field, sql string }{
		{"recipient_orphan", `select count(*) from mail_recipients r where not exists(select 1 from mail_messages m where m.rowid=r.message_rowid)`},
		{"attachment_orphan", `select count(*) from mail_attachments a where not exists(select 1 from mail_messages m where m.rowid=a.message_rowid)`},
		{"fts_orphan", `select count(*) from mail_fts f where not exists(select 1 from mail_messages m where m.rowid=f.rowid)`},
	} {
		rows := query(check.sql)
		if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != int64(0) {
			bad[check.field] = true
		}
	}
	return owaSortedFields(bad)
}

func owaTimeMatches(got, want any) bool {
	if want == nil {
		return got == nil
	}
	actual, aOK := parseTime(asString(got))
	expected, eOK := parseTime(asString(want))
	return aOK && eOK && actual.Equal(expected.Truncate(time.Millisecond))
}

func owaNumber(value any) int64 {
	n, ok := value.(int64)
	if !ok {
		return -1
	}
	return n
}

func owaHTMLMatches(value any, expected string) bool {
	if expected == "" {
		return value == nil
	}
	data, ok := value.(string)
	if !ok {
		return false
	}
	raw, err := hex.DecodeString(data)
	if err != nil {
		return false
	}
	r, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return false
	}
	// Bound decompression by the independently observed body length.
	decoded, readErr := io.ReadAll(io.LimitReader(r, int64(len(expected))+1))
	closeErr := r.Close()
	return readErr == nil && closeErr == nil && strings.Compare(string(decoded), expected) == 0
}
