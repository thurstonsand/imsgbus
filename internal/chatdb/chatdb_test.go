package chatdb_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/thurstonsand/imsgbus/internal/chatdb"
)

var appleEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

func apple(t time.Time) int64 { return int64(t.Sub(appleEpoch)) }

// fixture is a chat.db with the columns the watcher reads, written to the way Messages does:
// from another connection, in WAL mode.
func fixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat.db")
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	exec(t, db, `CREATE TABLE chat (ROWID INTEGER PRIMARY KEY, guid TEXT)`)
	exec(t, db, `CREATE TABLE chat_message_join (chat_id INTEGER, message_id INTEGER)`)
	exec(t, db, `CREATE TABLE message (ROWID INTEGER PRIMARY KEY, guid TEXT, is_from_me INTEGER,
		date INTEGER, date_delivered INTEGER DEFAULT 0, is_read INTEGER DEFAULT 0,
		date_read INTEGER DEFAULT 0, is_finished INTEGER DEFAULT 1)`)
	exec(t, db, `INSERT INTO chat VALUES (1, 'any;-;+15550001111')`)
	return path, db
}

func exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func insert(t *testing.T, db *sql.DB, id int64, fromMe bool, sent time.Time, delivered, read int64) {
	t.Helper()
	isRead := 0
	if read != 0 {
		isRead = 1
	}
	exec(t, db, `INSERT INTO message (ROWID, guid, is_from_me, date, date_delivered, is_read, date_read)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, fmt.Sprintf("g%d", id), fromMe, apple(sent), delivered, isRead, read)
	exec(t, db, `INSERT INTO chat_message_join VALUES (1, ?)`, id)
}

func TestWatchReportsDeliveriesAndReadsOnce(t *testing.T) {
	path, db := fixture(t)
	now := time.Now()
	readAt := apple(now)
	insert(t, db, 1, true, now.Add(-time.Hour), 0, 0)                // sent, not yet delivered
	insert(t, db, 2, false, now.Add(-time.Hour), 0, 0)               // received, unread
	insert(t, db, 3, false, now.Add(-time.Hour), 0, readAt)          // received and already read
	insert(t, db, 4, true, now.Add(-30*24*time.Hour), apple(now), 0) // too old to track

	changes := make(chan chatdb.Change, 16)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- chatdb.Watch(ctx, path, func(c chatdb.Change) { changes <- c }) }()
	time.Sleep(200 * time.Millisecond)

	exec(t, db, `UPDATE message SET date_delivered = ? WHERE ROWID IN (1, 4)`, readAt)
	exec(t, db, `UPDATE message SET is_read = 1, date_read = ? WHERE ROWID IN (1, 2, 3)`, readAt)
	insert(t, db, 5, true, now, readAt, 0) // delivered before the watcher first sees it

	want := []string{"delivered g1 from-me", "read g1 from-me", "read g2 to-me", "delivered g5 from-me"}
	got := map[string]bool{}
	for range want {
		select {
		case c := <-changes:
			kind := map[chatdb.Kind]string{chatdb.Delivered: "delivered", chatdb.Read: "read"}[c.Kind]
			side := map[bool]string{true: "from-me", false: "to-me"}[c.IsFromMe]
			key := fmt.Sprintf("%s %s %s", kind, c.GUID, side)
			if got[key] {
				t.Fatalf("reported %s twice", key)
			}
			got[key] = true
			if c.ChatGUID != "any;-;+15550001111" {
				t.Fatalf("%s is in chat %q", key, c.ChatGUID)
			}
		case err := <-done:
			t.Fatalf("Watch ended: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	for _, key := range want {
		if !got[key] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	exec(t, db, `UPDATE message SET is_read = 1, date_read = ? WHERE ROWID = 5`, readAt)
	exec(t, db, `UPDATE message SET is_read = 1 WHERE ROWID = 4`)
	select {
	case c := <-changes:
		if c.GUID != "g5" || c.Kind != chatdb.Read {
			t.Fatalf("unexpected %+v", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("read of g5 not reported")
	}
	select {
	case c := <-changes:
		t.Fatalf("unexpected %+v; g4 is outside the window", c)
	case <-time.After(time.Second):
	}
}
