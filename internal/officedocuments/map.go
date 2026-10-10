package officedocuments

import (
	"encoding/json"
	"strings"
)

type rowMapper struct {
	valid        bool
	unknownTimes int
}

func (m *rowMapper) text(object map[string]any, key string) string {
	if object[key] == nil {
		return ""
	}
	value, ok := object[key].(string)
	if !ok || strings.ContainsRune(value, 0) {
		m.valid = false
	}
	return value
}

func (m *rowMapper) object(object map[string]any, key string) map[string]any {
	if object[key] == nil {
		return nil
	}
	value, ok := object[key].(map[string]any)
	if !ok {
		m.valid = false
	}
	return value
}

func (m *rowMapper) integer(object map[string]any, key string, nonnegative bool) *int64 {
	if object[key] == nil {
		return nil
	}
	number, ok := object[key].(json.Number)
	if !ok {
		m.valid = false
		return nil
	}
	value, err := number.Int64()
	if err != nil || (nonnegative && value < 0) {
		m.valid = false
		return nil
	}
	return &value
}

func (m *rowMapper) boolean(object map[string]any, key string) *bool {
	if object[key] == nil {
		return nil
	}
	value, ok := object[key].(bool)
	if !ok {
		m.valid = false
		return nil
	}
	return &value
}

func (m *rowMapper) time(object map[string]any, key string) Timestamp {
	if object[key] == nil {
		return Timestamp{}
	}
	value, known := timestamp(m.text(object, key))
	if !known {
		m.unknownTimes++
	}
	return value
}

func (m *rowMapper) person(object map[string]any) Person {
	return Person{UPN: m.text(object, "upn"), DisplayName: m.text(object, "display_name")}
}

func mapDocument(value any, surface Surface) (Document, int, bool) {
	row, ok := value.(map[string]any)
	if !ok {
		return Document{}, 0, false
	}
	m := rowMapper{valid: true}
	d := Document{Surface: surface}
	if surface == Recent || surface == Recommended {
		d.Title, d.URL = m.text(row, "title"), m.text(row, "url")
		d.Extension, d.ResourceID = m.text(row, "extension"), m.text(row, "resource_id")
		d.Size, d.Pinned = m.integer(row, "file_size", true), m.boolean(row, "is_pinned")
		d.SharingState = m.integer(m.object(row, "sharing_info"), "state", false)
		if surface == Recent {
			d.Opened = m.time(row, "time_stamp")
		}
		drive := m.object(row, "onedrive_info")
		d.DriveID, d.ItemID = m.text(drive, "drive_id"), m.text(drive, "item_id")
		sp := m.object(row, "sharepoint_info")
		d.SharePoint = SharePoint{
			TenantID: m.text(sp, "tenant_id"), SiteID: m.text(sp, "site_id"), WebID: m.text(sp, "web_id"),
			ListID: m.text(sp, "list_id"), UniqueID: m.text(sp, "list_item_unique_id"),
		}
		d.SiteURL = m.text(sp, "site_url")
		d.SiteTitle = m.text(m.object(sp, "site_info"), "title")
		channel := m.object(sp, "teams_channel_info")
		d.TeamsChannelURL, d.TeamsChannelTitle = m.text(channel, "url"), m.text(channel, "title")
		created, modified := m.object(row, "creation_info"), m.object(row, "modification_info")
		d.Creator, d.Modifier = m.person(m.object(created, "user_info")), m.person(m.object(modified, "user_info"))
		d.Created, d.Modified = m.time(created, "timestamp"), m.time(modified, "timestamp")
		badge := m.object(m.object(row, "activity_info"), "badge")
		if badge != nil {
			d.Activity = &Activity{MessageFormat: m.text(badge, "message_format"), At: m.time(badge, "timestamp")}
			if badge["users"] != nil {
				users, ok := badge["users"].([]any)
				if !ok {
					m.valid = false
				}
				for _, value := range users {
					user, ok := value.(map[string]any)
					if !ok {
						m.valid = false
					}
					d.Activity.Users = append(d.Activity.Users, m.person(user))
				}
			}
		}
	} else {
		d.Title, d.URL = m.text(row, "FileName"), m.text(row, "DocumentUrl")
		d.ResourceID, d.Pinned = m.text(row, "ResourceId"), m.boolean(row, "IsPinned")
		if surface == Dialog {
			d.FriendlyPath, d.Opened = m.text(row, "Path"), m.time(row, "Timestamp")
		} else {
			d.WebURL, d.DriveID, d.ItemID = m.text(row, "WebUrl"), m.text(row, "DriveId"), m.text(row, "ItemId")
			sp := m.object(row, "SharepointIds")
			d.SharePoint = SharePoint{
				SiteID: m.text(sp, "SiteId"), WebID: m.text(sp, "WebId"), ListID: m.text(sp, "ListId"),
				ListItemID: m.text(sp, "ListItemId"), UniqueID: m.text(sp, "ListItemUniqueId"),
			}
			d.Modified = m.time(row, "LastModifiedDate")
			d.Sharing = &Sharing{DisplayName: m.text(row, "SharedByUserName"), Email: m.text(row, "SharedByUserEmail"),
				At: m.time(row, "SharedDate"), Type: m.integer(row, "SharingType", false)}
		}
	}
	return d, m.unknownTimes, m.valid && strings.TrimSpace(d.Title) != "" && strings.TrimSpace(d.URL) != ""
}

func documentStrings(d Document) []string {
	values := []string{
		d.Title, d.URL, d.WebURL, d.Extension, d.ResourceID, d.FriendlyPath, d.DriveID, d.ItemID,
		d.SharePoint.TenantID, d.SharePoint.SiteID, d.SharePoint.WebID, d.SharePoint.ListID,
		d.SharePoint.ListItemID, d.SharePoint.UniqueID, d.SiteURL, d.SiteTitle, d.TeamsChannelURL, d.TeamsChannelTitle,
		d.Creator.UPN, d.Creator.DisplayName, d.Modifier.UPN, d.Modifier.DisplayName,
		d.Opened.Raw, d.Created.Raw, d.Modified.Raw,
	}
	if d.Activity != nil {
		values = append(values, d.Activity.MessageFormat, d.Activity.At.Raw)
		for _, user := range d.Activity.Users {
			values = append(values, user.UPN, user.DisplayName)
		}
	}
	if d.Sharing != nil {
		values = append(values, d.Sharing.DisplayName, d.Sharing.Email, d.Sharing.At.Raw)
	}
	return values
}
