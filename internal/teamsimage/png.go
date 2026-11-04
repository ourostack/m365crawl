package teamsimage

import "context"

type PNGInfo struct {
	Width, Height int
	Size          int
	SHA256        string
	ContentType   string
}

func ValidatePNG(context.Context, []byte, string) (PNGInfo, error) {
	return PNGInfo{}, imageRefError("image_bytes_invalid")
}
