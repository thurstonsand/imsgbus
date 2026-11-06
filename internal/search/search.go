// Package search finds messages by their text. Backends are interchangeable, so FTS5 can give way
// to Spotlight or imsg's own search.
package search

import (
	"context"
	"time"
)

// Document is one message as a backend indexes it.
type Document struct {
	// RowID is the message's chat.db ROWID.
	RowID    int64
	GUID     string
	ChatGUID string
	Sender   string
	IsFromMe bool
	Time     time.Time
	Text     string
}

// Query asks for the messages matching Text. The other filters apply when set.
type Query struct {
	Text string
	// ChatGUIDs keeps messages in any of these chats.
	ChatGUIDs []string
	// From keeps messages sent from any of these handles; FromMe keeps the account's own.
	From   []string
	FromMe bool
	Since  time.Time
	Before time.Time
	Limit  int
	// Cursor continues an earlier search from its Results.Next.
	Cursor string
}

// Hit is one matching message, with a snippet of its text around the match.
type Hit struct {
	Document Document
	Snippet  string
}

// Results is one page of hits, newest first.
type Results struct {
	Hits []Hit
	// Next is the cursor of the following page, or empty on the last one.
	Next string
}

// Backend indexes messages and searches them.
type Backend interface {
	// Index adds documents, replacing any already indexed under the same row ID.
	Index(ctx context.Context, docs []Document) error
	// Remove drops the documents of the given message GUIDs.
	Remove(ctx context.Context, guids []string) error
	Search(ctx context.Context, q Query) (Results, error)
	// LastRowID is the highest row ID indexed, after which indexing resumes; 0 when empty.
	LastRowID(ctx context.Context) (int64, error)
}
