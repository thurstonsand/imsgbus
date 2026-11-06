package chatdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Member is one of the chats Messages keeps with a conversation's other side.
type Member struct {
	GUID string
	// RowID is the chat's row, which imsg calls its chat ID.
	RowID int64
	// Live is whether the chat has a message outside Recently Deleted.
	Live bool
	// Last is when its newest message arrived, or zero when it has none.
	Last time.Time
}

// DirectChats returns the GUIDs of every one-on-one chat.
func DirectChats(ctx context.Context, path string) ([]string, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT guid FROM chat WHERE instr(guid, ';-;') > 0`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var guids []string
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			return nil, err
		}
		guids = append(guids, guid)
	}
	return guids, rows.Err()
}

// Members returns the chats with the given GUIDs, newest message first.
func Members(ctx context.Context, path string, guids []string) ([]Member, error) {
	list, err := json.Marshal(guids)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT c.guid, c.ROWID, coalesce(max(m.date), 0) FROM chat c
		LEFT JOIN chat_message_join j ON j.chat_id = c.ROWID
		LEFT JOIN message m ON m.ROWID = j.message_id
		WHERE c.guid IN (SELECT value FROM json_each(?))
		GROUP BY c.ROWID ORDER BY 3 DESC, c.ROWID DESC`, string(list))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var members []Member
	for rows.Next() {
		var m Member
		var date int64
		if err := rows.Scan(&m.GUID, &m.RowID, &date); err != nil {
			return nil, err
		}
		if date != 0 {
			m.Live = true
			m.Last = appleTime(date)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}
