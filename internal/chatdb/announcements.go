package chatdb

import (
	"context"
	"database/sql"
	"encoding/json"

	"howett.net/plist"
)

// AnnouncementKind is the change to a group that a message row records.
type AnnouncementKind int

const (
	// Added is someone added to the group.
	Added AnnouncementKind = iota
	// Removed is someone removed from the group.
	Removed
	// Renamed is the group getting a new name.
	Renamed
	// NameRemoved is the group losing its name.
	NameRemoved
	// Left is someone leaving the group.
	Left
	// PhotoChanged is the group getting a new photo.
	PhotoChanged
	// PhotoRemoved is the group losing its photo.
	PhotoRemoved
)

// Announcement is a message row that records a change to a group instead of content. imsg reports
// such a row as a message with neither text nor files.
type Announcement struct {
	Kind AnnouncementKind
	// Actor is the handle that made the change, or empty when you did.
	Actor string
	// Unattributed is a change Messages shows without anyone making it, such as an RCS group's
	// server removing someone.
	Unattributed bool
	// Subject is the handle added or removed.
	Subject string
	// Title is the group's new name.
	Title string
}

// Announcements returns the announcements among the messages with the given GUIDs, by GUID.
//
// The codes follow imessage-exporter's reading of chat.db: item_type 1 is a member added (group
// action 0) or removed (1), 2 a name change, 3 someone leaving (0) or the photo changing (1) or
// going (2), and message_summary_info's "uat" marks a change shown without an actor.
func Announcements(ctx context.Context, path string, guids []string) (map[string]Announcement, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	list, err := json.Marshal(guids)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT m.guid, m.item_type, coalesce(m.group_action_type, 0), coalesce(m.handle_id, 0),
		coalesce(h.id, ''), coalesce(m.other_handle, 0), coalesce(o.id, ''), coalesce(m.group_title, ''), m.message_summary_info
		FROM message m
		LEFT JOIN handle h ON h.ROWID = m.handle_id
		LEFT JOIN handle o ON o.ROWID = m.other_handle
		WHERE m.item_type IN (1, 2, 3) AND m.guid IN (SELECT value FROM json_each(?))`, string(list))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	found := map[string]Announcement{}
	for rows.Next() {
		var guid, actor, subject, title string
		var item, action, actorID, subjectID int64
		var summary []byte
		if err = rows.Scan(&guid, &item, &action, &actorID, &actor, &subjectID, &subject, &title, &summary); err != nil {
			return nil, err
		}
		a := Announcement{Actor: actor, Subject: subject, Title: title}
		switch {
		// An "added" row naming its own sender is someone changing their number.
		case item == 1 && action == 0 && subjectID != actorID:
			a.Kind = Added
		case item == 1 && action == 1:
			a.Kind = Removed
		case item == 2 && title != "":
			a.Kind = Renamed
		case item == 2:
			a.Kind = NameRemoved
		case item == 3 && action == 0:
			a.Kind = Left
		case item == 3 && action == 1:
			a.Kind = PhotoChanged
		case item == 3 && action == 2:
			a.Kind = PhotoRemoved
		default:
			continue
		}
		// Someone leaving is always theirs to make.
		if a.Kind != Left {
			if a.Unattributed, err = unattributed(summary); err != nil {
				return nil, err
			}
		}
		found[guid] = a
	}
	return found, rows.Err()
}

func unattributed(summary []byte) (bool, error) {
	if len(summary) == 0 {
		return false, nil
	}
	var info map[string]any
	if _, err := plist.Unmarshal(summary, &info); err != nil {
		return false, err
	}
	uat, _ := info["uat"].(bool)
	return uat, nil
}
