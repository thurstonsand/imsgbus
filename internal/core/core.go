// Package core owns this Mac's Messages session and serves it to every frontend in imsgbus's own
// types. Frontends never see imsg's JSON or chat.db rows.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/thurstonsand/imsgbus/internal/imsg"
)

// Message is one new message in any chat, from any of the account's devices.
type Message struct {
	// RowID orders messages within this Mac's database; watches resume after it.
	RowID    int64
	GUID     string
	ChatGUID string
	IsGroup  bool
	Sender   string
	IsFromMe bool
	Text     string
	Time     time.Time
	ReplyTo  string
}

// Chat is one conversation.
type Chat struct {
	GUID         string
	Name         string
	IsGroup      bool
	Participants []string
}

// Account is the Apple Account Messages is signed in to.
type Account struct {
	ID     string
	Handle string
}

// Service is the single owner of the `imsg rpc` child.
type Service struct {
	log    zerolog.Logger
	client *imsg.Client

	lock  sync.Mutex
	chats map[string]Chat
	// chatIDs maps a chat's GUID to its row ID, which some imsg methods take instead.
	chatIDs map[string]int64
	names   map[string]string
	// queues holds each watch's undelivered notifications. A subscription's notifications can
	// arrive before Watch learns its ID, so they queue under that ID either way, and the stdout
	// reader never waits on a slow watch.
	queues map[int64]*queue
}

type notification struct {
	method string
	params json.RawMessage
}

type queue struct {
	items []notification
	wake  chan struct{}
}

func newQueue() *queue { return &queue{wake: make(chan struct{}, 1)} }

// Start spawns imsg.
func Start(log zerolog.Logger, imsgPath string) (*Service, error) {
	s := &Service{
		log:     log,
		chats:   make(map[string]Chat),
		chatIDs: make(map[string]int64),
		names:   make(map[string]string),
		queues:  make(map[int64]*queue),
	}
	client, err := imsg.Start(log.With().Str("component", "imsg").Logger(), imsgPath, s.notify)
	if err != nil {
		return nil, err
	}
	s.client = client
	return s, nil
}

// Exited is closed if the imsg child dies; nothing in core recovers from that.
func (s *Service) Exited() <-chan struct{} { return s.client.Exited() }

// Err is why the imsg child died.
func (s *Service) Err() error { return s.client.Err() }

// Account reports the Apple Account of the most recently active chat that names one.
func (s *Service) Account(ctx context.Context) (Account, error) {
	chats, err := s.client.ChatsList(ctx, 20)
	if err != nil {
		return Account{}, err
	}
	for _, chat := range chats {
		if chat.AccountID != "" && chat.LastHandle != "" {
			return Account{ID: chat.AccountID, Handle: chat.LastHandle}, nil
		}
	}
	return Account{}, errors.New("no recent chat names the Messages account; send or receive one message first")
}

// OverflowError ends a watch that fell too far behind; resume after ResumeAfter.
type OverflowError struct{ ResumeAfter int64 }

func (e *OverflowError) Error() string {
	return fmt.Sprintf("watch fell behind; resume after row %d", e.ResumeAfter)
}

// Watch calls handle for every new message after sinceRowID, or from now when it is 0, until ctx
// ends, imsg dies, or the watch overflows or fails.
func (s *Service) Watch(ctx context.Context, sinceRowID int64, handle func(Message)) error {
	id, err := s.client.WatchSubscribe(ctx, sinceRowID)
	if err != nil {
		return err
	}
	s.lock.Lock()
	q, ok := s.queues[id]
	if !ok {
		q = newQueue()
		s.queues[id] = q
	}
	s.lock.Unlock()
	defer func() {
		s.lock.Lock()
		delete(s.queues, id)
		s.lock.Unlock()
		if err := s.client.WatchUnsubscribe(context.WithoutCancel(ctx), id); err != nil {
			s.log.Warn().Err(err).Int64("subscription", id).Msg("Failed to unsubscribe watch")
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.client.Exited():
			return s.client.Err()
		case <-q.wake:
		}
		s.lock.Lock()
		items := q.items
		q.items = nil
		s.lock.Unlock()
		for _, n := range items {
			if err := s.deliver(n, handle); err != nil {
				return err
			}
		}
	}
}

func (s *Service) deliver(n notification, handle func(Message)) error {
	switch n.method {
	case imsg.NotifyOverflow:
		var o imsg.OverflowNotification
		if err := json.Unmarshal(n.params, &o); err != nil {
			return fmt.Errorf("decode watch overflow: %w", err)
		}
		return &OverflowError{ResumeAfter: o.ResumeAfterRowID}
	case imsg.NotifyError:
		var e imsg.ErrorNotification
		if err := json.Unmarshal(n.params, &e); err != nil {
			return fmt.Errorf("decode watch error: %w", err)
		}
		return fmt.Errorf("imsg ended the watch: %s", e.Error.Message)
	}
	var m imsg.MessageNotification
	if err := json.Unmarshal(n.params, &m); err != nil {
		return fmt.Errorf("decode watch message: %w", err)
	}
	if m.Message.Type != "" {
		s.log.Debug().Str("type", m.Message.Type).Str("guid", m.Message.GUID).Msg("Ignoring message mutation")
		return nil
	}
	handle(s.remember(m.Message))
	return nil
}

// NewMessageGUID returns a GUID for SendText, for callers that must know it before sending.
func NewMessageGUID() string { return uuid.NewString() }

// SendText sends text to a chat as a new message with the given GUID from NewMessageGUID.
func (s *Service) SendText(ctx context.Context, chatGUID, guid, text, replyTo string) error {
	return s.client.SendTracked(ctx, chatGUID, guid, text, replyTo)
}

// Chat looks up a chat by GUID among the most recently active.
func (s *Service) Chat(ctx context.Context, guid string) (Chat, error) {
	s.lock.Lock()
	chat, ok := s.chats[guid]
	s.lock.Unlock()
	if ok {
		return chat, nil
	}
	chats, err := s.client.ChatsList(ctx, 500)
	if err != nil {
		return Chat{}, err
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	for _, c := range chats {
		s.chats[c.GUID] = Chat{GUID: c.GUID, Name: c.DisplayName, IsGroup: c.IsGroup, Participants: c.Participants}
		s.chatIDs[c.GUID] = c.ID
		if !c.IsGroup && c.ContactName != "" && len(c.Participants) == 1 {
			s.names[c.Participants[0]] = c.ContactName
		}
	}
	chat, ok = s.chats[guid]
	if !ok {
		return Chat{}, fmt.Errorf("chat %s is not among the %d most recent", guid, len(chats))
	}
	return chat, nil
}

// Name is the contact name last seen for a handle, or the handle itself.
func (s *Service) Name(handle string) string {
	s.lock.Lock()
	defer s.lock.Unlock()
	if name, ok := s.names[handle]; ok {
		return name
	}
	return handle
}

// readCheckDepth is how far back MarkReadThrough looks for the receipted message.
const readCheckDepth = 50

// MarkReadThrough marks a chat read for a reader who has seen everything up to the message with
// the given GUID. imsg can only mark a whole chat read, so it does nothing while a newer message
// from someone else exists that the reader hasn't seen, and reports whether it marked the chat.
func (s *Service) MarkReadThrough(ctx context.Context, chatGUID, guid string) (bool, error) {
	if _, err := s.Chat(ctx, chatGUID); err != nil {
		return false, err
	}
	s.lock.Lock()
	chatID := s.chatIDs[chatGUID]
	s.lock.Unlock()
	messages, err := s.client.History(ctx, chatID, readCheckDepth)
	if err != nil {
		return false, err
	}
	seen, newestInbound := int64(-1), int64(0)
	for _, m := range messages {
		if m.GUID == guid {
			seen = m.ID
		}
		if !m.IsFromMe {
			newestInbound = max(newestInbound, m.ID)
		}
	}
	if seen < 0 || newestInbound > seen {
		return false, nil
	}
	return true, s.client.Read(ctx, chatGUID)
}

func (s *Service) notify(method string, params json.RawMessage) {
	if method != imsg.NotifyMessage && method != imsg.NotifyOverflow && method != imsg.NotifyError {
		s.log.Debug().Str("method", method).Msg("Ignoring imsg notification")
		return
	}
	var envelope struct {
		Subscription int64 `json:"subscription"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		s.log.Error().Err(err).Str("method", method).Msg("Undecodable watch notification")
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	q, ok := s.queues[envelope.Subscription]
	if !ok {
		q = newQueue()
		s.queues[envelope.Subscription] = q
	}
	q.items = append(q.items, notification{method: method, params: params})
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (s *Service) remember(m imsg.Message) Message {
	s.lock.Lock()
	s.chatIDs[m.ChatGUID] = m.ChatID
	if !m.IsFromMe && m.SenderName != "" {
		s.names[m.Sender] = m.SenderName
	}
	s.lock.Unlock()
	// imsg keeps U+FFFC where an attachment sits in the text; attachments aren't text.
	text := strings.TrimSpace(strings.ReplaceAll(m.Text, "\uFFFC", ""))
	return Message{
		RowID:    m.ID,
		GUID:     m.GUID,
		ChatGUID: m.ChatGUID,
		IsGroup:  m.IsGroup,
		Sender:   m.Sender,
		IsFromMe: m.IsFromMe,
		Text:     text,
		Time:     m.CreatedAt,
		ReplyTo:  m.ReplyToGUID,
	}
}
