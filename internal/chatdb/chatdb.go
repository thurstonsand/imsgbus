// Package chatdb reads Messages' chat.db, read-only, for what imsg doesn't report: when a message
// was delivered and when it was read, on either side, when a chat was deleted or came back, and
// what change to a group a row records.
package chatdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	_ "github.com/mattn/go-sqlite3" // registers the "sqlite3" driver
)

// Kind is what happened to a message.
type Kind int

const (
	// Delivered means your message reached the recipient's devices.
	Delivered Kind = iota
	// Read means the recipient read your message, or you read theirs on any of your devices.
	Read
)

// Change is a message's delivery or read state moving forward.
type Change struct {
	Kind     Kind
	RowID    int64
	GUID     string
	ChatGUID string
	IsFromMe bool
	Time     time.Time
}

// pendingWindow is how long an outbound message may still be delivered or read and be reported.
const pendingWindow = 7 * 24 * time.Hour

// fallbackPoll catches writes the file watch misses, such as across a WAL checkpoint that
// replaces the file.
const fallbackPoll = 5 * time.Second

// appleEpoch is chat.db's time zero; its dates count nanoseconds from it.
var appleEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

func appleTime(ns int64) time.Time { return appleEpoch.Add(time.Duration(ns)) }

func appleNanos(t time.Time) int64 { return int64(t.Sub(appleEpoch)) }

type row struct {
	guid      string
	chatGUID  string
	isFromMe  bool
	date      int64
	delivered int64
	isRead    bool
	read      int64
}

// settled rows can't change in a way worth reporting any more.
func (r row) settled(cutoff int64) bool {
	return r.isRead || (r.isFromMe && r.date < cutoff)
}

// Watch reports every delivery and read of a recent message until ctx ends. Changes that happen
// while nothing watches are not reported.
func Watch(ctx context.Context, path string, report func(Change)) error {
	w := &watch{report: report, pending: make(map[int64]row)}
	return follow(ctx, path, w.start, w.check)
}

// follow calls start on a read-only connection to chat.db, then check whenever another connection
// has written to it, until ctx ends or either fails.
func follow(ctx context.Context, path string, start, check func(context.Context, *sql.Conn) error) error {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	// data_version only moves for writes by other connections, as seen from one connection.
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = watcher.Close() }()
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		return err
	}

	var version int64
	if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version); err != nil {
		return err
	}
	if err := start(ctx, conn); err != nil {
		return err
	}
	ticker := time.NewTicker(fallbackPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-watcher.Errors:
			return err
		case evt := <-watcher.Events:
			if !strings.HasPrefix(filepath.Base(evt.Name), filepath.Base(path)) {
				continue
			}
		case <-ticker.C:
		}
		var now int64
		if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&now); err != nil {
			return err
		}
		if now == version {
			continue
		}
		version = now
		if err := check(ctx, conn); err != nil {
			return err
		}
	}
}

type watch struct {
	report  func(Change)
	pending map[int64]row
	maxRow  int64
}

const rowColumns = `m.ROWID, m.guid, c.guid, m.is_from_me, m.date, m.date_delivered, m.is_read, m.date_read
	FROM message m
	JOIN chat_message_join j ON j.message_id = m.ROWID
	JOIN chat c ON c.ROWID = j.chat_id`

func (w *watch) start(ctx context.Context, conn *sql.Conn) error {
	if err := conn.QueryRowContext(ctx, "SELECT coalesce(max(ROWID), 0) FROM message").Scan(&w.maxRow); err != nil {
		return err
	}
	// The second clause matches Messages' own partial index of unread messages.
	return each(ctx, conn, `SELECT `+rowColumns+`
		WHERE (m.is_from_me = 1 AND m.date > ? AND m.is_read = 0)
		   OR (m.is_read = 0 AND m.is_finished = 1 AND m.is_from_me = 0)`,
		[]any{cutoff()}, func(id int64, r row) { w.pending[id] = r })
}

func cutoff() int64 { return appleNanos(time.Now().Add(-pendingWindow)) }

// check re-reads only what may have changed: rows added since the last check, and the pending
// rows by primary key.
func (w *watch) check(ctx context.Context, conn *sql.Conn) error {
	if len(w.pending) > 0 {
		ids := make([]string, 0, len(w.pending))
		for id := range w.pending {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		err := each(ctx, conn, `SELECT `+rowColumns+` WHERE m.ROWID IN (`+strings.Join(ids, ",")+`)`, nil,
			func(id int64, r row) { w.update(id, w.pending[id], r) })
		if err != nil {
			return err
		}
	}
	// A new row may already be delivered or read by the time it is seen.
	return each(ctx, conn, `SELECT `+rowColumns+` WHERE m.ROWID > ?`, []any{w.maxRow}, func(id int64, r row) {
		w.maxRow = max(w.maxRow, id)
		w.update(id, row{isFromMe: r.isFromMe}, r)
	})
}

func (w *watch) update(id int64, was, now row) {
	if now.isFromMe && was.delivered == 0 && now.delivered != 0 {
		w.report(Change{Kind: Delivered, RowID: id, GUID: now.guid, ChatGUID: now.chatGUID, IsFromMe: true, Time: appleTime(now.delivered)})
	}
	if !was.isRead && now.isRead {
		at := time.Now()
		if now.read != 0 {
			at = appleTime(now.read)
		}
		w.report(Change{Kind: Read, RowID: id, GUID: now.guid, ChatGUID: now.chatGUID, IsFromMe: now.isFromMe, Time: at})
	}
	if now.settled(cutoff()) {
		delete(w.pending, id)
	} else {
		w.pending[id] = now
	}
}

func each(ctx context.Context, conn *sql.Conn, query string, args []any, fn func(int64, row)) error {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var r row
		if err := rows.Scan(&id, &r.guid, &r.chatGUID, &r.isFromMe, &r.date, &r.delivered, &r.isRead, &r.read); err != nil {
			return err
		}
		fn(id, r)
	}
	return rows.Err()
}
