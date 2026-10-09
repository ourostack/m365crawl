package cli

import (
	"encoding/json"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

// storeItem is one object store of one database in the generic archive.
type storeItem struct {
	Database      string    `json:"database"`
	Store         string    `json:"store"`
	Records       int       `json:"records"`
	Removed       int       `json:"removed"`
	LastUpdatedAt time.Time `json:"last_updated_at,omitzero"`
}

type storesCmd struct{}

func (c *storesCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "stores", ""); err != nil {
		return err
	}
	rt.query = listQuery{none: "the archive holds no generic stores yet: run m365crawl sync"}
	return rt.read("stores", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		rows, err := st.Stores(rt.ctx, rt.account)
		if err != nil {
			return nil, err
		}
		items := make([]storeItem, len(rows))
		for i, r := range rows {
			items[i] = storeItem(r)
		}
		return newList(shape(rt, items), false), nil
	})
}

// recordItem is one archived record of a database that has no typed table. key_json and
// value_json are the parsed JSON of the record's key and value, not strings of JSON; value_json is
// absent when the value could not be decoded. With --max-text, a value whose JSON text is longer
// becomes a JSON string of its first characters and text_truncated is true.
type recordItem struct {
	Source        string          `json:"source"`
	TenantID      string          `json:"tenant_id,omitempty"`
	UserID        string          `json:"user_id,omitempty"`
	Database      string          `json:"database"`
	Store         string          `json:"store"`
	KeyJSON       json.RawMessage `json:"key_json"`
	ValueJSON     json.RawMessage `json:"value_json,omitempty"`
	FirstSeenAt   time.Time       `json:"first_seen_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	RemovedAt     time.Time       `json:"removed_at,omitzero"`
	TextTruncated bool            `json:"text_truncated,omitempty"`
}

func recordItems(rows []store.RecordRow, maxText int) []recordItem {
	items := make([]recordItem, len(rows))
	for i, r := range rows {
		it := recordItem{Source: r.Source, TenantID: r.TenantID, UserID: r.UserID, Database: r.Database, Store: r.Store,
			KeyJSON: json.RawMessage(r.KeyJSON), FirstSeenAt: r.FirstSeenAt, UpdatedAt: r.UpdatedAt, RemovedAt: r.RemovedAt}
		if r.ValueJSON != "" {
			it.ValueJSON = json.RawMessage(r.ValueJSON)
			if text, cut := truncateRunes(r.ValueJSON, maxText); cut {
				it.ValueJSON, _ = json.Marshal(text)
				it.TextTruncated = true
			}
		}
		items[i] = it
	}
	return items
}

type recordsCmd struct {
	Database       string `required:"" help:"Database name or a prefix of it, for example Teams:calendar-manager. See the stores command for the names."`
	Store          string `help:"Only this object store, matched exactly."`
	Since          string `help:"Only records changed at or after this time (RFC3339, YYYY-MM-DD or a relative duration such as 24h)."`
	IncludeRemoved bool   `name:"include-removed" help:"Also include records Teams' cache no longer holds (they keep their last value and have removed_at set)."`
	Limit          int    `default:"50" help:"Maximum items to return; truncated says whether more exist."`
}

func (c *recordsCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "records", ""); err != nil {
		return err
	}
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	since, err := rt.when("--since", c.Since)
	if err != nil {
		return err
	}
	rt.query = listQuery{filtered: true}
	return rt.read("records", func(st *store.Store) (result, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		var total int
		rows, trunc, err := st.Records(rt.ctx, store.RecordFilter{Account: rt.account, Database: c.Database, Store: c.Store, Since: since, IncludeRemoved: c.IncludeRemoved, Limit: c.Limit, Total: &total})
		if err != nil {
			return nil, err
		}
		return newList(shape(rt, recordItems(rows, rt.g.MaxText)), trunc).withTotal(total), nil
	})
}
