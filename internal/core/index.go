package core

import (
	"context"
	"time"

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
			if evt.Text != "" {
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
	return search.Document{RowID: rowID, GUID: guid, ChatGUID: chatGUID, Sender: contacts.Normalize(sender), IsFromMe: isFromMe, Time: at, Text: text}
}
