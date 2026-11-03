package chatdb

import (
	"context"
	"database/sql"
)

// ChatState is a chat leaving the conversation list or coming back to it.
type ChatState struct {
	GUID    string
	Deleted bool
}

// WatchChats reports each chat that is deleted and each deleted chat that comes back, until ctx
// ends. It starts by reporting every chat that is already deleted.
//
// Deleting a conversation moves its messages to Recently Deleted and keeps the chat's row, so a
// chat is deleted once no message is left in it, and comes back when a message is recovered or a
// new one arrives. A chat whose row lands before its first message comes back the same way.
func WatchChats(ctx context.Context, path string, report func(ChatState)) error {
	// known maps each chat's GUID to whether it has a message.
	var known map[string]bool
	return follow(ctx, path, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		if known, err = chats(ctx, conn); err != nil {
			return err
		}
		for guid, live := range known {
			if !live {
				report(ChatState{GUID: guid, Deleted: true})
			}
		}
		return nil
	}, func(ctx context.Context, conn *sql.Conn) error {
		now, err := chats(ctx, conn)
		if err != nil {
			return err
		}
		for guid, was := range known {
			if live, ok := now[guid]; was && !live {
				report(ChatState{GUID: guid, Deleted: true})
			} else if ok && !was && live {
				report(ChatState{GUID: guid})
			}
		}
		known = now
		return nil
	})
}

// chats maps every chat's GUID to whether it has a message.
func chats(ctx context.Context, conn *sql.Conn) (map[string]bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT c.guid,
		EXISTS (SELECT 1 FROM chat_message_join j WHERE j.chat_id = c.ROWID) FROM chat c`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	live := map[string]bool{}
	for rows.Next() {
		var guid string
		var hasMessages bool
		if err := rows.Scan(&guid, &hasMessages); err != nil {
			return nil, err
		}
		live[guid] = hasMessages
	}
	return live, rows.Err()
}
