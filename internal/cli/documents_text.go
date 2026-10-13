package cli

import (
	"encoding/json"
	"fmt"
	"time"
)

func (rt *runtime) renderDocumentDetails(result *listResult) error {
	for _, item := range result.Items {
		detail, ok := item.(documentDetailItem)
		if !ok {
			continue
		}
		if detail.RegistryNative != nil {
			rt.renderRegistryDetail(detail)
			continue
		}
		if detail.UsageNative != nil {
			rt.renderUsageDetail(detail)
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
		_, _ = fmt.Fprintf(rt.stdout, "surface %s: %s\nassociation %s: %s\nclocks: %s\n", detail.Surface, documentSurfaces[detail.Surface],
			detail.AssociationState, documentAssociationHelp, documentClocksHelp)
		if detail.Native.Sharing != nil {
			_, _ = fmt.Fprintln(rt.stdout, "Sharing.Type is Office's SharingType number as written, kept unmapped")
		}
	}
	return nil
}

// renderRegistryDetail prints an Office registry row: its native claims, its two clocks in
// words, and what the row does not prove. It names no surface, because a registry row has none.
func (rt *runtime) renderRegistryDetail(detail documentDetailItem) {
	encoded, _ := json.MarshalIndent(detail.RegistryNative, "", "  ") // strings, an integer and nullable scalars always encode
	opened := "unknown (the registry gave none, or registry_native.OpenedRaw is not an RFC 3339 time with an offset)"
	if detail.OpenedAt.At != nil {
		opened = detail.OpenedAt.At.UTC().Format(time.RFC3339Nano)
	}
	_, _ = fmt.Fprintf(rt.stdout, "\n%s: %s\nsource %d; Office registry entry; association %s; text %s\n",
		detail.ID, detail.Title, detail.SourceID, detail.AssociationState, detail.TextState)
	_, _ = fmt.Fprintf(rt.stdout, "registry native claims:\n%s\n", encoded)
	_, _ = fmt.Fprintf(rt.stdout, "opened_at: %s\nmodified_at: unknown (the registry states no modification time)\n", opened)
	_, _ = fmt.Fprintf(rt.stdout, "URL reference occurrences: %d (returned %d; truncated=%t)\n",
		detail.ReferencesTotal, len(detail.References), detail.ReferencesTruncated)
	_, _ = fmt.Fprintf(rt.stdout, "association %s: %s\nThe document, its URL and its friendly path were never opened, checked or resolved; this is the registry's metadata only.\n",
		detail.AssociationState, documentAssociationHelp)
}

// renderUsageDetail prints a OneDrive usage row: what kind of record it is, its bounded native
// claims, its modification time and preview counts in words, its relations, and what the row
// does not prove. It names no surface, and offers no thread or person command for a claim.
func (rt *runtime) renderUsageDetail(detail documentDetailItem) {
	encoded, _ := json.MarshalIndent(detail.UsageNative, "", "  ") // strings, integers, booleans, times and nullable scalars always encode
	lines := usageDetailLines(detail)
	_, _ = fmt.Fprintf(rt.stdout, "\n%s: %s\nsource %d; %s; association %s; text %s\n",
		detail.ID, usageLabel(detail.documentItem), detail.SourceID, lines[0], detail.AssociationState, detail.TextState)
	_, _ = fmt.Fprintf(rt.stdout, "usage native claims:\n%s\n", encoded)
	for _, line := range lines[1:] {
		_, _ = fmt.Fprintln(rt.stdout, line)
	}
	_, _ = fmt.Fprintf(rt.stdout, "URL reference occurrences: %d (returned %d; truncated=%t)\n",
		detail.ReferencesTotal, len(detail.References), detail.ReferencesTruncated)
	_, _ = fmt.Fprintf(rt.stdout, "association %s: %s\nNames, addresses, thread IDs and collaborator profiles are the usage file's claims, not linked to an archived person, chat or account. The document and its URL were never opened, checked or resolved; this is OneDrive's local metadata only.\n",
		detail.AssociationState, documentAssociationHelp)
}
