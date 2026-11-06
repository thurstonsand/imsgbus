package core

import (
	"context"
	"runtime"
	"time"

	"github.com/thurstonsand/imsgbus/internal/chatdb"
	"github.com/thurstonsand/imsgbus/internal/contacts"
	"github.com/thurstonsand/imsgbus/internal/search"
)

// indexPage is how many messages one catch-up page indexes, imsg's most for messages.after.
const indexPage = 500

const indexRetryDelay = 5 * time.Second

// RunIndex keeps the search index current until ctx ends: it catches up on the messages the index
// hasn't seen, then follows new messages, edits and unsends as they happen.
func (s *Service) RunIndex(ctx context.Context) {
	for ctx.Err() == nil {
		err := s.index(ctx)
		if ctx.Err() != nil {
			return
		}
		s.log.Err(err).Msg("Search indexing stopped; retrying")
		select {
		case <-ctx.Done():
		case <-time.After(indexRetryDelay):
		}
	}
}

func (s *Service) index(ctx context.Context) error {
	cursor, err := s.search.LastRowID(ctx)
	if err != nil {
		return err
	}
	start, indexed := time.Now(), 0
	for {
		messages, next, more, err := s.client.MessagesAfter(ctx, cursor, indexPage)
		if err != nil {
			return err
		}
		docs := make([]search.Document, 0, len(messages))
		for _, m := range messages {
			if msg := toMessage(m); msg.Text != "" {
				docs = append(docs, document(msg.RowID, msg.GUID, msg.ChatGUID, msg.Sender, msg.IsFromMe, msg.Time, msg.Text))
			}
		}
		if err := s.search.Index(ctx, docs); err != nil {
			return err
		}
		indexed += len(docs)
		cursor = next
		if !more {
			break
		}
	}
	s.log.Info().Int("indexed", indexed).Dur("took", time.Since(start)).Int64("cursor", cursor).Msg("Search index caught up")
	return s.Watch(ctx, cursor, false, func(evt Event) {
		var err error
		switch evt := evt.(type) {
		case Message:
			// Catching up never sees group changes, so the watch leaves them out too.
			if evt.Text != "" && !evt.Announcement {
				err = s.search.Index(ctx, []search.Document{document(evt.RowID, evt.GUID, evt.ChatGUID, evt.Sender, evt.IsFromMe, evt.Time, evt.Text)})
			}
		case Edit:
			err = s.search.Index(ctx, []search.Document{document(evt.RowID, evt.GUID, evt.ChatGUID, evt.Sender, evt.IsFromMe, evt.Time, evt.Text)})
		case Unsend:
			err = s.search.Remove(ctx, []string{evt.GUID})
		}
		if err != nil {
			s.log.Err(err).Msg("Failed to update search index")
		}
	})
}

func document(rowID int64, guid, chatGUID, sender string, isFromMe bool, at time.Time, text string) search.Document {
	address, _ := Address(sender)
	return search.Document{RowID: rowID, GUID: guid, ChatGUID: chatGUID, Sender: contacts.Normalize(address), IsFromMe: isFromMe, Time: at, Text: text}
}

// heartbeatEvery is how often RunHeartbeat reports, so a quiet log still shows imsgbus alive.
const heartbeatEvery = 5 * time.Minute

// RunHeartbeat logs how far behind chat.db the search index is, and how much imsgbus holds, every
// heartbeatEvery until ctx ends.
func (s *Service) RunHeartbeat(ctx context.Context) {
	started := time.Now()
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		entry := s.log.Info().Str("event", "heartbeat")
		if row, err := chatdb.LastRowID(ctx, s.chatDB); err != nil {
			entry = entry.AnErr("chat_db_error", err)
		} else {
			entry = entry.Int64("chat_db_row", row)
		}
		if row, err := s.search.LastRowID(ctx); err != nil {
			entry = entry.AnErr("index_error", err)
		} else {
			entry = entry.Int64("index_row", row)
		}
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		entry.Int("contacts", s.contacts.Len()).Int("goroutines", runtime.NumGoroutine()).
			Uint64("heap_mb", mem.HeapAlloc>>20).Int64("uptime_s", int64(time.Since(started).Seconds())).Msg("Heartbeat")
	}
}
