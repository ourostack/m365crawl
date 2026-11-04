package fileprivacy

import (
	"os"

	"github.com/ourostack/m365crawl/internal/errs"
)

func Parent(*os.File) error    { return privateError() }
func Temporary(*os.File) error { return privateError() }

func privateError() error {
	return &errs.Coded{Code: "image_output_unprotected", Exit: errs.ExitEnvironment,
		Message: "the selected image output is not verified private",
		Fix:     "Choose an existing private parent directory; no access-control settings were changed."}
}
