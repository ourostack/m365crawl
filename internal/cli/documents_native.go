package cli

import (
	"encoding/json"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

type documentNative officedocuments.Document

type documentNativeTime struct {
	Raw   string
	Value *time.Time
}

type documentNativeActivity struct {
	*officedocuments.Activity
	At documentNativeTime
}

type documentNativeSharing struct {
	*officedocuments.Sharing
	At documentNativeTime
}

func documentNativeTimeOf(value officedocuments.Timestamp) documentNativeTime {
	return documentNativeTime{Raw: value.Raw, Value: searchTime(value.Value)}
}

func (d documentNative) MarshalJSON() ([]byte, error) {
	type plain documentNative
	var activity *documentNativeActivity
	if d.Activity != nil {
		activity = &documentNativeActivity{Activity: d.Activity, At: documentNativeTimeOf(d.Activity.At)}
	}
	var sharing *documentNativeSharing
	if d.Sharing != nil {
		sharing = &documentNativeSharing{Sharing: d.Sharing, At: documentNativeTimeOf(d.Sharing.At)}
	}
	return json.Marshal(struct {
		plain
		Opened, Created, Modified documentNativeTime
		Activity                  *documentNativeActivity
		Sharing                   *documentNativeSharing
	}{
		plain: plain(d), Opened: documentNativeTimeOf(d.Opened), Created: documentNativeTimeOf(d.Created),
		Modified: documentNativeTimeOf(d.Modified), Activity: activity, Sharing: sharing,
	})
}
