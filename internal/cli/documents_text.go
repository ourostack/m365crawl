package cli

import (
	"encoding/json"
	"fmt"
)

func (rt *runtime) renderDocumentDetails(result *listResult) error {
	for _, item := range result.Items {
		detail, ok := item.(documentDetailItem)
		if !ok {
			continue
		}
		encoded, err := json.MarshalIndent(detail.Native, "", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(rt.stdout, "\n%s: %s\nsource %d; %s/%s; association %s; text %s\n",
			detail.ID, detail.Title, detail.SourceID, detail.Application, detail.Surface, detail.AssociationState, detail.TextState)
		_, _ = fmt.Fprintf(rt.stdout, "native source claims:\n%s\n", encoded)
		_, _ = fmt.Fprintf(rt.stdout, "URL reference occurrences: %d (returned %d; truncated=%t)\n",
			detail.ReferencesTotal, len(detail.References), detail.ReferencesTruncated)
	}
	return nil
}
