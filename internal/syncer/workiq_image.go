package syncer

import (
	"context"
	"time"

	"github.com/ourostack/m365crawl/internal/imageexport"
	"github.com/ourostack/m365crawl/internal/teamsimage"
	"github.com/ourostack/m365crawl/internal/workiq"
)

type WorkIQImageOptions struct {
	DBPath, Account, ConversationID, MessageID, ImageID, Output string
	Invocation                                                  workiq.Invocation
}

type WorkIQImageReport struct {
	Account, ConversationID, MessageID, ImageID, Output string
	Status, Code                                        string
	AdditionalCodes                                     []string
	Info                                                teamsimage.PNGInfo
	NativeMessageModifiedAt                             *time.Time
	NativeBodyObservedAt                                *time.Time
	VerificationObservedAt                              *time.Time
	FetchedAt                                           *time.Time
	Effect                                              imageexport.Effect
}

var imageReserve = imageexport.Reserve
var imagePublish = (*imageexport.Reservation).Publish
var imageAbort = (*imageexport.Reservation).Abort

func FetchWorkIQChatImage(context.Context, WorkIQImageOptions) (WorkIQImageReport, error) {
	return WorkIQImageReport{}, workIQFailure("image_acquisition_unavailable")
}
