package cli

import "github.com/ourostack/m365crawl/internal/store"

func currentDocumentsCoverage(rt *runtime, st *store.Store) (*noteCoverage, error) {
	sources, err := st.DocumentSources(rt.ctx)
	if err != nil {
		return nil, err
	}
	coverage := documentsCoverage(sources)
	if rt.documentCoverage != nil {
		coverage.RefreshError = rt.documentCoverage.RefreshError
	}
	return coverage, nil
}

func mergedOptionalCoverage(notes, documents *noteCoverage) *noteCoverage {
	if notes == nil {
		return documents
	}
	if documents == nil {
		return notes
	}
	out := &noteCoverage{Scope: "registered_optional_sources", State: "unknown", Sources: []noteSourceItem{},
		RefreshErrors: map[string]syncError{}}
	out.Sources = append(out.Sources, notes.Sources...)
	out.Sources = append(out.Sources, documents.Sources...)
	if len(out.Sources) > 0 {
		out.State = "registered"
	}
	if notes.RefreshError != nil {
		copy := *notes.RefreshError
		out.RefreshErrors["notes"] = copy
		out.RefreshError = &copy
	}
	if documents.RefreshError != nil {
		out.RefreshErrors["documents"] = *documents.RefreshError
		if out.RefreshError == nil {
			copy := *documents.RefreshError
			out.RefreshError = &copy
		} else {
			out.RefreshError.Message += "; documents: " + documents.RefreshError.Code + ": " + documents.RefreshError.Message
		}
	}
	return out
}
