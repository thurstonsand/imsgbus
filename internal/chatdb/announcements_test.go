package chatdb_test

import (
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thurstonsand/imsgbus/internal/chatdb"
)

func TestAnnouncementsReadsGroupChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	exec(t, db, `CREATE TABLE handle (ROWID INTEGER PRIMARY KEY, id TEXT)`)
	exec(t, db, `CREATE TABLE message (ROWID INTEGER PRIMARY KEY, guid TEXT, item_type INTEGER,
		group_action_type INTEGER, handle_id INTEGER, other_handle INTEGER, group_title TEXT,
		message_summary_info BLOB)`)
	exec(t, db, `INSERT INTO handle VALUES (1, '+15550001111'), (2, '+15550002222')`)
	// {"uat": true}, as Messages stored it on an RCS group's server removing someone.
	uat, err := hex.DecodeString("62706c6973743030d101025375617409080b0f0000000000000101000000000000000300000000000000000000000000000010")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range [][]any{
		{"text", 0, 0, 1, 0, nil, nil},
		{"added", 1, 0, 1, 2, nil, nil},
		{"removed-by-server", 1, 1, 0, 2, nil, uat},
		{"number-changed", 1, 0, 2, 2, nil, nil},
		{"renamed-by-me", 2, 0, 0, 0, "Fam", nil},
		{"name-removed", 2, 0, 1, 0, "", nil},
		{"left", 3, 0, 2, 0, nil, uat},
		{"photo", 3, 1, 1, 0, nil, nil},
		{"photo-removed", 3, 2, 0, 0, nil, nil},
		{"background", 3, 4, 1, 0, nil, nil},
	} {
		exec(t, db, `INSERT INTO message (guid, item_type, group_action_type, handle_id, other_handle, group_title,
			message_summary_info) VALUES (?, ?, ?, ?, ?, ?, ?)`, row...)
	}

	got, err := chatdb.Announcements(t.Context(), path, []string{
		"text", "added", "removed-by-server", "number-changed", "renamed-by-me", "name-removed", "left", "photo", "photo-removed", "background",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]chatdb.Announcement{
		"added":             {Kind: chatdb.Added, Actor: "+15550001111", Subject: "+15550002222"},
		"removed-by-server": {Kind: chatdb.Removed, Unattributed: true, Subject: "+15550002222"},
		"renamed-by-me":     {Kind: chatdb.Renamed, Title: "Fam"},
		"name-removed":      {Kind: chatdb.NameRemoved, Actor: "+15550001111"},
		// Leaving is always the leaver's, whatever Messages flags.
		"left":          {Kind: chatdb.Left, Actor: "+15550002222"},
		"photo":         {Kind: chatdb.PhotoChanged, Actor: "+15550001111"},
		"photo-removed": {Kind: chatdb.PhotoRemoved},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
}
