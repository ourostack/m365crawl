package sharepointcontent

type Request struct {
	Kind, URL, TranscriptID string
}

type ReadError struct {
	Code       string
	HTTPStatus int
}

func (e *ReadError) Error() string {
	return "sharepoint_content: " + e.Code
}
