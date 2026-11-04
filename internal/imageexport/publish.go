package imageexport

import (
	"context"
	"os"

	"github.com/ourostack/m365crawl/internal/errs"
)

type RetainedTemp struct {
	State           string
	LastKnownPath   string
	LocatorVerified *bool
}

type Effect struct {
	Phase          string
	FilePublished  *bool
	OutputVerified *bool
	RetainedTemp   *RetainedTemp
	Codes          []string
}

type Reservation struct{}

var (
	imageWrite     = (*os.File).Write
	imageSync      = (*os.File).Sync
	imageClose     = (*os.File).Close
	imageLink      = (*os.Root).Link
	imageRemove    = (*os.Root).Remove
	imageRootClose = (*os.Root).Close
)

func Reserve(context.Context, string) (*Reservation, Effect, error) {
	effect, err := imageRefusal()
	return nil, effect, err
}

func (*Reservation) Publish(context.Context, []byte) (Effect, error) { return imageRefusal() }
func (*Reservation) Abort() (Effect, error)                          { return imageRefusal() }

func imageRefusal() (Effect, error) {
	no := false
	return Effect{FilePublished: &no, OutputVerified: &no},
		&errs.Coded{Code: "image_output_unprotected", Exit: errs.ExitEnvironment,
			Message: "the selected image output cannot be reserved privately",
			Fix:     "Choose an existing private output parent; no file was published."}
}
