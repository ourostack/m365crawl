package store

import (
	"context"
	"errors"
)

// usageRelationPreview is how many related observations a detail returns; the total is exact.
const usageRelationPreview = 100

// usageNativeOrder compares full literal components before any SQL limit or output clipping.
// It does not split native_key: admitted ID and Format text can contain its legacy delimiter.
const usageNativeOrder = `coalesce(json_extract(d.usage_json,'$.Type'),''),
coalesce(json_extract(d.usage_json,'$.Document.ID'),json_extract(d.usage_json,'$.Recent.ID'),json_extract(d.usage_json,'$.Collaborator.ID'),''),
coalesce(json_extract(d.usage_json,'$.Document.Format'),'')`

// readUsageRelations binds a usage row's relations to the capture that owns them: the capture
// whose membership the source's admitted capture has (its own, or the one an unchanged capture
// names) when the row is in it, otherwise the last capture whose own membership held the row. A recent
// row relates to every formatted variant of that capture with its native ID, and a variant to its
// recent row; a collaborator has none. An edge to a missing or mistyped observation is an error.
func readUsageRelations(ctx context.Context, reader knowledgeReader, detail *DocumentObservationDetail) error {
	row := detail.Observation
	detail.UsageRelations = []DocumentUsageRelation{}
	if row.Usage == nil || row.Usage.Type == UsageTypeCollaborator {
		return nil
	}
	owner, err := documentMembersOwner(ctx, reader, "o")
	if err != nil {
		return err
	}
	if err := reader.QueryRowContext(ctx, `select coalesce(
(select m.capture_id from knowledge_sources s join document_capture_metadata o on o.capture_id=s.admitted_attempt
 join document_capture_members m on m.capture_id=`+owner+` and m.observation_rowid=? where s.id=?),
(select max(capture_id) from document_capture_members where observation_rowid=?),0)`,
		row.ID, row.SourceID, row.ID).Scan(&detail.UsageRelationsCaptureID); err != nil {
		return err
	}
	own, related, want := "recent_rowid", "document_rowid", UsageTypeDocument
	if row.Usage.Type == UsageTypeDocument {
		own, related, want = "document_rowid", "recent_rowid", UsageTypeRecent
	}
	if err := reader.QueryRowContext(ctx, `select count(*) from document_usage_relations where capture_id=? and `+own+`=?`,
		detail.UsageRelationsCaptureID, row.ID).Scan(&detail.UsageRelationsTotal); err != nil {
		return err
	}
	rows, err := reader.QueryContext(ctx, `select r.`+related+`,coalesce(json_extract(d.usage_json,'$.Type'),''),
coalesce(json_extract(d.usage_json,'$.Document.ID'),json_extract(d.usage_json,'$.Recent.ID'),''),
coalesce(json_extract(d.usage_json,'$.Document.Format'),'')
from document_usage_relations r left join document_observations d on d.rowid=r.`+related+` and d.source_id=?
where r.capture_id=? and r.`+own+`=? order by `+usageNativeOrder+`,r.`+related+` limit ?`,
		row.SourceID, detail.UsageRelationsCaptureID, row.ID, usageRelationPreview)
	if err != nil {
		return err
	}
	for rows.Next() {
		var relation DocumentUsageRelation
		if err := rows.Scan(&relation.ObservationID, &relation.Type, &relation.ID, &relation.Format); err != nil {
			_ = rows.Close()
			return err
		}
		if relation.Type != want || relation.ID != usageNativeID(*row.Usage) {
			_ = rows.Close()
			return errors.New("documents: invalid stored usage relation")
		}
		detail.UsageRelations = append(detail.UsageRelations, relation)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	detail.UsageRelationsTruncated = detail.UsageRelationsTotal > len(detail.UsageRelations)
	return nil
}
