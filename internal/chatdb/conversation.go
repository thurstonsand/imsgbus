package chatdb

import (
	"context"
	"database/sql"
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

// Members returns the chats whose GUID is id or id with a category in parentheses after it, as
// Messages' text message filter names the chat of a message it sorted, newest message first.
func Members(ctx context.Context, path, id string) ([]Member, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT c.guid, c.ROWID, coalesce(max(m.date), 0) FROM chat c
		LEFT JOIN chat_message_join j ON j.chat_id = c.ROWID
		LEFT JOIN message m ON m.ROWID = j.message_id
		WHERE c.guid = ? OR substr(c.guid, 1, length(?) + 1) = ? || '('
		GROUP BY c.ROWID ORDER BY 3 DESC, c.ROWID DESC`, id, id, id)
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
