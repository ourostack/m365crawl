package store

import (
	"context"
	"errors"
	"strings"
)

// schemaFeature is a feature whose archive state a presence check reads: the schema version that
// added it, its <Feature>SchemaVersion constant and the tables it adds.
type schemaFeature struct {
	name     string
	constant string
	version  int
	tables   []string
}

var (
	workIQFeature = schemaFeature{"WorkIQ archive state", "WorkIQSchemaVersion", WorkIQSchemaVersion, []string{"workiq_accounts",
		"workiq_attempts", "workiq_frontiers", "workiq_pages", "workiq_rows", "workiq_aliases", "workiq_source_keys", "workiq_folders",
		"workiq_censuses", "workiq_census_members", "workiq_gaps", "workiq_mail_receipts"}}
	workIQDocumentFeature = schemaFeature{"WorkIQ document archive state", "WorkIQDocumentSchemaVersion", WorkIQDocumentSchemaVersion,
		[]string{"workiq_documents", "workiq_document_checks", "workiq_document_bodies", "workiq_document_fts"}}
	directedWriteFeature = schemaFeature{"directed write log", "DirectedWriteSchemaVersion", DirectedWriteSchemaVersion,
		[]string{"directed_writes", "directed_write_events"}}
	directedDraftFeature = schemaFeature{"directed write drafts", "DirectedDraftSchemaVersion", DirectedDraftSchemaVersion,
		[]string{"directed_write_drafts"}}
)

// schemaFeatures lists every <Feature>SchemaVersion, one entry per line, so two changes that add
// one conflict here in Git. TestSchemaFeatureNumbers fails when a constant is missing from it, two
// share a number or one is above SchemaVersion, and CI's schema number check (scripts/schemaguard)
// fails when a pull request adds or moves one to a number its base branch's SchemaVersion has
// already used (#306). A new feature constant takes the next number above the base branch's
// SchemaVersion, raises SchemaVersion with it, and gets an entry here.
var schemaFeatures = []schemaFeature{
	workIQFeature,
	workIQDocumentFeature,
	directedWriteFeature,
	directedDraftFeature,
}

// featureState reads the archive's recorded schema version and how many of f's tables it has.
func featureState(ctx context.Context, q rowQuerier, f schemaFeature) (version, tables int, err error) {
	args := make([]any, len(f.tables))
	for i, t := range f.tables {
		args[i] = t
	}
	err = q.QueryRowContext(ctx, `select coalesce(max(version),0),(select count(*) from sqlite_master where type='table' and name in (`+
		strings.TrimSuffix(strings.Repeat("?,", len(f.tables)), ",")+`)) from schema_migrations`, args...).Scan(&version, &tables)
	return version, tables, err
}

// hasFeature says whether the archive has f, deciding by its tables. The recorded version only
// rules f out: an archive recorded below f's number was never given it. An archive recorded at or
// above it with none of f's tables does not have it either, because parallel changes can reorder
// schema numbers so that a version was reached without f (#306). Some but not all of its tables
// is an error: the state is broken, not absent.
func hasFeature(ctx context.Context, q rowQuerier, f schemaFeature) (bool, error) {
	version, tables, err := featureState(ctx, q, f)
	if err != nil || version < f.version || tables == 0 {
		return false, err
	}
	if tables != len(f.tables) {
		return false, errors.New(f.name + " is incomplete")
	}
	return true, nil
}
