package workiq

import (
	"context"
	"encoding/json"
	"time"
)

type ImageMessage struct {
	ModifiedAt *time.Time
}

type ChatImageRef struct {
	resource string
}

type ImageBlob struct {
	Data        []byte
	ContentType string
	HTTPStatus  int
}

func DecodeSelectedImageMessage(context.Context, string, string, json.RawMessage) (ImageMessage, error) {
	return ImageMessage{}, failure("workiq_protocol")
}

func BindChatImage(ImageMessage, string) (ChatImageRef, error) {
	return ChatImageRef{}, failure("workiq_protocol")
}

func SameImageObservation(ImageMessage, ImageMessage, string) error {
	return failure("workiq_protocol")
}

func (*Client) FetchChatImageBlob(context.Context, ChatImageRef) (ImageBlob, error) {
	return ImageBlob{}, failure("workiq_protocol")
}

func decodeImageBlob(context.Context, []byte) (ImageBlob, error) {
	return ImageBlob{}, failure("workiq_protocol")
}
