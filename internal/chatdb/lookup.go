package chatdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// File is an attachment of a message, at Path on this Mac.
type File struct {
	Path string
	MIME string
}

// ErrNoMessage is a message GUID chat.db doesn't have.
var ErrNoMessage = errors.New("no such message")

// MessageChat returns the GUID of the chat a message is in, and the message's files.
func MessageChat(ctx context.Context, path, guid string) (string, []File, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = db.Close() }()
	var chatGUID string
	err = db.QueryRowContext(ctx, `SELECT c.guid FROM message m
		JOIN chat_message_join j ON j.message_id = m.ROWID
		JOIN chat c ON c.ROWID = j.chat_id
		WHERE m.guid = ?`, guid).Scan(&chatGUID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, fmt.Errorf("%w: %s", ErrNoMessage, guid)
	} else if err != nil {
		return "", nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT coalesce(a.filename, ''), coalesce(a.mime_type, '') FROM message m
		JOIN message_attachment_join j ON j.message_id = m.ROWID
		JOIN attachment a ON a.ROWID = j.attachment_id
		WHERE m.guid = ? ORDER BY a.ROWID`, guid)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = rows.Close() }()
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	var files []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.Path, &f.MIME); err != nil {
			return "", nil, err
		}
		if rest, ok := strings.CutPrefix(f.Path, "~/"); ok {
			f.Path = filepath.Join(home, rest)
		}
		files = append(files, f)
	}
	return chatGUID, files, rows.Err()
}
