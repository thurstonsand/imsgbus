package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3" // registers the "sqlite3" driver; FTS5 needs the sqlite_fts5 build tag
)

// The trigram tokenizer matches any substring, so partial words ("burger" in "cheeseburger"), the
// middle of a name, and emoji next to text all match; remove_diacritics makes "cafe" match "café".
// Its one gap, terms shorter than 3 characters, is reported as an error instead of matching nothing.
const schema = `
CREATE TABLE IF NOT EXISTS messages (
	row_id INTEGER PRIMARY KEY,
	guid TEXT NOT NULL UNIQUE,
	chat_guid TEXT NOT NULL,
	sender TEXT NOT NULL,
	is_from_me INTEGER NOT NULL,
	time INTEGER NOT NULL,
	text TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_time ON messages (time, row_id);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
	text, content = 'messages', content_rowid = 'row_id',
	tokenize = 'trigram remove_diacritics 1'
);
CREATE TRIGGER IF NOT EXISTS messages_insert AFTER INSERT ON messages BEGIN
	INSERT INTO messages_fts (rowid, text) VALUES (new.row_id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_delete AFTER DELETE ON messages BEGIN
	INSERT INTO messages_fts (messages_fts, rowid, text) VALUES ('delete', old.row_id, old.text);
END;
CREATE TRIGGER IF NOT EXISTS messages_update AFTER UPDATE ON messages BEGIN
	INSERT INTO messages_fts (messages_fts, rowid, text) VALUES ('delete', old.row_id, old.text);
	INSERT INTO messages_fts (rowid, text) VALUES (new.row_id, new.text);
END;
`

// FTS5 is a Backend over SQLite's FTS5, in a database file of its own.
type FTS5 struct {
	db *sql.DB
}

var _ Backend = (*FTS5)(nil)

// OpenFTS5 opens the index at path, creating it if needed.
func OpenFTS5(path string) (*FTS5, error) {
	// A URI's path must be absolute, or it reads as the authority.
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Writes take the lock up front, so a write never fails upgrading a read lock another writer holds.
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_journal_mode=WAL&_txlock=immediate"}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("creating the search index at %s: %w", path, err)
	}
	return &FTS5{db: db}, nil
}

// Close closes the index.
func (f *FTS5) Close() error {
	return f.db.Close()
}

// Index implements Backend.
func (f *FTS5) Index(ctx context.Context, docs []Document) error {
	return f.inTx(ctx, func(tx *sql.Tx) error {
		for _, d := range docs {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO messages (row_id, guid, chat_guid, sender, is_from_me, time, text)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (row_id) DO UPDATE SET guid = excluded.guid, chat_guid = excluded.chat_guid,
					sender = excluded.sender, is_from_me = excluded.is_from_me, time = excluded.time,
					text = excluded.text`,
				d.RowID, d.GUID, d.ChatGUID, d.Sender, d.IsFromMe, d.Time.UnixNano(), d.Text)
			if err != nil {
				return fmt.Errorf("indexing message %s: %w", d.GUID, err)
			}
		}
		return nil
	})
}

// Remove implements Backend.
func (f *FTS5) Remove(ctx context.Context, guids []string) error {
	return f.inTx(ctx, func(tx *sql.Tx) error {
		for _, guid := range guids {
			if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE guid = ?`, guid); err != nil {
				return err
			}
		}
		return nil
	})
}

// LastRowID implements Backend.
func (f *FTS5) LastRowID(ctx context.Context) (int64, error) {
	var last int64
	err := f.db.QueryRowContext(ctx, `SELECT coalesce(max(row_id), 0) FROM messages`).Scan(&last)
	return last, err
}

// Search implements Backend.
func (f *FTS5) Search(ctx context.Context, q Query) (Results, error) {
	match, err := matchExpr(q.Text)
	if err != nil {
		return Results{}, err
	}
	if q.Limit <= 0 {
		return Results{}, errors.New("a search needs a positive limit")
	}
	since, before := int64(math.MinInt64), int64(math.MaxInt64)
	if !q.Since.IsZero() {
		since = q.Since.UnixNano()
	}
	if !q.Before.IsZero() {
		before = q.Before.UnixNano()
	}
	// The cursor is the time and row ID of the previous page's last hit; ordering by both keeps
	// messages sharing a timestamp from being skipped or repeated across pages.
	afterTime, afterRow := int64(math.MaxInt64), int64(math.MaxInt64)
	if q.Cursor != "" {
		if _, err = fmt.Sscanf(q.Cursor, "%d:%d", &afterTime, &afterRow); err != nil {
			return Results{}, fmt.Errorf("malformed search cursor %q", q.Cursor)
		}
	}
	from, err := json.Marshal(append([]string{}, q.From...))
	if err != nil {
		return Results{}, err
	}
	rows, err := f.db.QueryContext(ctx, `
		SELECT m.row_id, m.guid, m.chat_guid, m.sender, m.is_from_me, m.time, m.text,
			snippet(messages_fts, 0, '', '', '…', 64)
		FROM messages_fts JOIN messages m ON m.row_id = messages_fts.rowid
		WHERE messages_fts MATCH @match
			AND (@chat = '' OR m.chat_guid = @chat)
			AND (@from = '[]' OR m.sender IN (SELECT value FROM json_each(@from)))
			AND (NOT @from_me OR m.is_from_me)
			AND m.time >= @since AND m.time < @before
			AND (m.time, m.row_id) < (@after_time, @after_row)
		ORDER BY m.time DESC, m.row_id DESC
		LIMIT @limit`,
		sql.Named("match", match), sql.Named("chat", q.ChatGUID), sql.Named("from", string(from)), sql.Named("from_me", q.FromMe),
		sql.Named("since", since), sql.Named("before", before),
		sql.Named("after_time", afterTime), sql.Named("after_row", afterRow),
		// One extra row tells whether another page follows.
		sql.Named("limit", q.Limit+1))
	if err != nil {
		return Results{}, err
	}
	defer func() { _ = rows.Close() }()
	var res Results
	for rows.Next() {
		var h Hit
		var nanos int64
		if err := rows.Scan(&h.Document.RowID, &h.Document.GUID, &h.Document.ChatGUID, &h.Document.Sender,
			&h.Document.IsFromMe, &nanos, &h.Document.Text, &h.Snippet); err != nil {
			return Results{}, err
		}
		h.Document.Time = time.Unix(0, nanos)
		res.Hits = append(res.Hits, h)
	}
	if err := rows.Err(); err != nil {
		return Results{}, err
	}
	if len(res.Hits) > q.Limit {
		res.Hits = res.Hits[:q.Limit]
		last := res.Hits[q.Limit-1].Document
		res.Next = fmt.Sprintf("%d:%d", last.Time.UnixNano(), last.RowID)
	}
	return res, nil
}

// matchExpr turns query text into an FTS5 expression: every word, and every "quoted phrase", must
// appear somewhere in the message.
func matchExpr(text string) (string, error) {
	var terms []string
	// Odd parts sit between quotes; an unclosed quote runs to the end.
	for i, part := range strings.Split(text, `"`) {
		if i%2 == 1 {
			if phrase := strings.TrimSpace(part); phrase != "" {
				terms = append(terms, phrase)
			}
			continue
		}
		terms = append(terms, strings.Fields(part)...)
	}
	if len(terms) == 0 {
		return "", errors.New("a search needs text")
	}
	quoted := make([]string, len(terms))
	for i, term := range terms {
		if utf8.RuneCountInString(term) < 3 {
			return "", fmt.Errorf("search term %q is shorter than 3 characters; quote it with the words around it", term)
		}
		quoted[i] = `"` + term + `"`
	}
	return strings.Join(quoted, " "), nil
}

func (f *FTS5) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
