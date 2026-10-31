// Package directedwrites carries out the person's directed writes through WorkIQ under
// contracts/directed-actions/v0: the writes switch, the decision on live state, the write-ahead
// log, one create call, and verification by reading back.
package directedwrites

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

// Switch is the person's writes switch, a file beside the archive.
type Switch struct {
	On        bool
	ChangedAt time.Time
}

type switchFile struct {
	Writes    string `json:"writes"`
	ChangedAt string `json:"changed_at"`
}

// SwitchPath is the switch file of the archive at db.
func SwitchPath(db string) string { return db + ".writes" }

// ReadSwitch fails closed: a missing, unreadable or malformed file means off.
func ReadSwitch(db string) Switch {
	raw, err := os.ReadFile(SwitchPath(db)) //nolint:gosec // G304: the selected archive's own switch file.
	if err != nil {
		return Switch{}
	}
	var f switchFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&f) != nil || f.Writes != "on" && f.Writes != "off" {
		return Switch{}
	}
	changed, _ := time.Parse(time.RFC3339Nano, f.ChangedAt)
	return Switch{On: f.Writes == "on", ChangedAt: changed}
}

// now is the package clock; a test seam.
var now = time.Now

// SetSwitch writes the switch beside an archive directory that already exists, so it never
// creates the archive's private directory with weaker permissions. Only the person's confirmed
// terminal command calls it.
func SetSwitch(db string, on bool) (Switch, error) {
	value := "off"
	if on {
		value = "on"
	}
	at := now().UTC()
	raw, _ := json.Marshal(switchFile{Writes: value, ChangedAt: at.Format(time.RFC3339Nano)})
	path := SwitchPath(db)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return Switch{}, errs.DBError(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return Switch{}, errs.DBError(errors.Join(err, os.Remove(tmp)))
	}
	return Switch{On: on, ChangedAt: at}, nil
}
