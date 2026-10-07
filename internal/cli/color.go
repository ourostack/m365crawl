package cli

import (
	"errors"
	"os"
	"strconv"

	"github.com/ourostack/m365crawl/internal/render"
)

// colorEnabled decides ANSI color for stdout. --no-color and NO_COLOR always win. Otherwise color
// is on for a terminal, or when CLICOLOR_FORCE is set to anything but "" or "0" (for screenshots
// and for pagers that understand ANSI).
func (rt *runtime) colorEnabled() bool {
	if rt.g.NoColor || os.Getenv("NO_COLOR") != "" {
		return false
	}
	if v := os.Getenv("CLICOLOR_FORCE"); v != "" && v != "0" {
		return true
	}
	f, ok := rt.stdout.(*os.File)
	return ok && render.ColorEnabled(f, false)
}

// termWidth is the text-mode line width: the terminal's, else $COLUMNS, else 100.
func (rt *runtime) termWidth() int {
	if f, ok := rt.stdout.(*os.File); ok {
		if w := fileWidth(f); w > 0 {
			return w
		}
	}
	if w, err := atoiPositive(os.Getenv("COLUMNS")); err == nil {
		return w
	}
	return 100
}

func atoiPositive(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err == nil && n <= 0 {
		err = errors.New("not positive")
	}
	return n, err
}
