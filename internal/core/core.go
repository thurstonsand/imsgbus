// Package core owns this Mac's Messages session and serves it to every frontend in imsgbus's own
// types. Frontends never see imsg's JSON or chat.db rows.
package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/thurstonsand/imsgbus/internal/chatdb"
	"github.com/thurstonsand/imsgbus/internal/contacts"
	"github.com/thurstonsand/imsgbus/internal/imsg"
	"github.com/thurstonsand/imsgbus/internal/search"
)

// Chat is one conversation. Participants excludes this account.
type Chat struct {
	GUID         string
	Name         string
	IsGroup      bool
	Participants []string
	LastMessage  time.Time
	// Unread is set by RecentChats and UnreadChats only.
	Unread int
}

func toChat(c imsg.Chat) Chat {
	name := c.DisplayName
	if name == "" && c.Name != c.Identifier {
		// chats.get has no display_name, and both name an unnamed chat by its identifier.
		name = c.Name
	}
	return Chat{GUID: c.GUID, Name: strings.TrimSpace(name), IsGroup: c.IsGroup, Participants: c.Participants, LastMessage: c.LastMessage, Unread: c.Unread}
}

// Account is the Apple Account Messages is signed in to.
type Account struct {
	ID     string
	Handle string
}

// Options is what a Service runs on.
type Options struct {
	IMsgPath string
	ChatDB   string
	Contacts *contacts.Catalog
	Search   search.Backend
}

// Service is the single owner of the `imsg rpc` child.
type Service struct {
	log      zerolog.Logger
	client   *imsg.Client
	chatDB   string
	contacts *contacts.Catalog
	search   search.Backend

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
func Start(log zerolog.Logger, opts Options) (*Service, error) {
	s := &Service{
		log:      log,
		chatDB:   opts.ChatDB,
		contacts: opts.Contacts,
		search:   opts.Search,
		chats:    make(map[string]Chat),
		chatIDs:  make(map[string]int64),
		names:    make(map[string]string),
		queues:   make(map[int64]*queue),
	}
	client, err := imsg.Start(log.With().Str("component", "imsg").Logger(), opts.IMsgPath, s.notify)
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
	chats, err := s.client.ChatsList(ctx, 20, false)
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

// Watch calls handle for every new message or tapback after sinceRowID, or from now when it is 0,
// and every edit and unsend from now, with attachments when withFiles is set, until ctx ends, imsg dies, or the watch overflows or fails.
// Edits and unsends made while nothing watched are lost.
func (s *Service) Watch(ctx context.Context, sinceRowID int64, withFiles bool, handle func(Event)) error {
	id, err := s.client.WatchSubscribe(ctx, sinceRowID, withFiles)
	if err != nil {
		return err
	}
	// Each watch tells its own handler about a group changing, so it compares against what it
	// has seen itself.
	seen := map[string]Chat{}
	return s.pump(ctx, id, func(n notification) error {
		var m imsg.MessageNotification
		if err := json.Unmarshal(n.params, &m); err != nil {
			return fmt.Errorf("decode watch message: %w", err)
		}
		if change, changed := s.remember(m.Message, seen); changed {
			handle(change)
		}
		evt := toEvent(m.Message)
		if msg, ok := evt.(Message); ok {
			announced := []Message{msg}
			s.announce(ctx, announced)
			evt = announced[0]
		}
		handle(evt)
		return nil
	})
}

// announce describes each message that records a change to a group, which imsg reports with
// neither text nor files, from chat.db.
func (s *Service) announce(ctx context.Context, messages []Message) {
	var guids []string
	for _, m := range messages {
		if m.Text == "" && len(m.Attachments) == 0 && m.App == "" {
			guids = append(guids, m.GUID)
		}
	}
	if len(guids) == 0 {
		return
	}
	found, err := chatdb.Announcements(ctx, s.chatDB, guids)
	if err != nil {
		// Failing the watch would retry the same row forever and stop every chat; these messages
		// just stay as they were before imsgbus read group changes.
		s.log.Err(err).Strs("guids", guids).Msg("Failed to read group changes; leaving them unsupported")
		return
	}
	for i, m := range messages {
		a, ok := found[m.GUID]
		if !ok {
			continue
		}
		m.Announcement = true
		m.Text = announcementText(a, s.Name)
		// imsg names your own number, as someone else, for a change chat.db records without a sender.
		m.Sender, m.IsFromMe = a.Actor, m.IsFromMe || a.Actor == ""
		if a.Unattributed {
			m.Sender, m.IsFromMe = "", false
		}
		messages[i] = m
	}
}

// pump passes subscription id's notifications to deliver, in order, until ctx ends, imsg dies, or
// a terminal notification ends the subscription.
func (s *Service) pump(ctx context.Context, id int64, deliver func(notification) error) error {
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
			s.log.Warn().Err(err).Int64("subscription", id).Msg("Failed to unsubscribe")
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
			if err := terminal(n); err != nil {
				return err
			}
			if err := deliver(n); err != nil {
				return err
			}
		}
	}
}

// terminal is the error a subscription-ending notification carries, or nil for any other.
func terminal(n notification) error {
	switch n.method {
	case imsg.NotifyOverflow:
		var o imsg.OverflowNotification
		if err := json.Unmarshal(n.params, &o); err != nil {
			return fmt.Errorf("decode watch overflow: %w", err)
		}
		return &OverflowError{ResumeAfter: o.ResumeAfterRowID}
	case imsg.NotifyError, imsg.NotifyBridgeError:
		var e imsg.ErrorNotification
		if err := json.Unmarshal(n.params, &e); err != nil {
			return fmt.Errorf("decode %s: %w", n.method, err)
		}
		return fmt.Errorf("imsg ended subscription with %s: %s", n.method, e.Error.Message)
	case imsg.NotifyBridgeOverflow:
		return errors.New("imsg bridge events fell behind")
	}
	return nil
}

// Typing is someone else starting or stopping typing in a one-on-one chat.
type Typing struct {
	ChatGUID string
	IsTyping bool
}

// WatchTyping calls handle whenever the other side of a one-on-one chat starts or stops typing.
func (s *Service) WatchTyping(ctx context.Context, handle func(Typing)) error {
	id, err := s.client.BridgeEventsSubscribe(ctx)
	if err != nil {
		return err
	}
	return s.pump(ctx, id, func(n notification) error {
		var e imsg.BridgeEventNotification
		if err := json.Unmarshal(n.params, &e); err != nil {
			return fmt.Errorf("decode bridge event: %w", err)
		}
		typing := e.Event.Event == "started-typing"
		if !typing && e.Event.Event != "stopped-typing" {
			return nil
		}
		// Bridge events name the chat by service ("iMessage;-;+1555…") where chat.db's GUID may
		// say "any;-;+1555…"; the rest identifies it.
		_, rest, _ := strings.Cut(e.Event.Data.ChatGUID, ";")
		s.lock.Lock()
		var guid string
		for g := range s.chatIDs {
			if _, r, _ := strings.Cut(g, ";"); r == rest {
				guid = g
			}
		}
		s.lock.Unlock()
		if guid == "" || !strings.HasPrefix(rest, "-;") {
			s.log.Debug().Str("chat_guid", e.Event.Data.ChatGUID).Msg("Ignoring typing in a group or unseen chat")
			return nil
		}
		handle(Typing{ChatGUID: guid, IsTyping: typing})
		return nil
	})
}

// SetTyping shows or clears the typing indicator in a chat.
func (s *Service) SetTyping(ctx context.Context, chatGUID string, typing bool) error {
	return s.client.Typing(ctx, chatGUID, typing)
}

// ReceiptKind is what a receipt says happened to a message.
type ReceiptKind = chatdb.Kind

// Receipt kinds.
const (
	Delivered = chatdb.Delivered
	Read      = chatdb.Read
)

// Receipt is a message's delivery or read: of your message by its recipient, or of an incoming
// message by you on any device.
type Receipt struct {
	Kind     ReceiptKind
	RowID    int64
	GUID     string
	ChatGUID string
	// IsFromMe is whose message it is: true for yours, read or delivered by the other side.
	IsFromMe bool
	Time     time.Time
}

// WatchReceipts calls handle for every delivery and read of a recent message until ctx ends.
func (s *Service) WatchReceipts(ctx context.Context, handle func(Receipt)) error {
	return chatdb.Watch(ctx, s.chatDB, func(c chatdb.Change) {
		handle(Receipt{Kind: c.Kind, RowID: c.RowID, GUID: c.GUID, ChatGUID: c.ChatGUID, IsFromMe: c.IsFromMe, Time: c.Time})
	})
}

// ChatState is a chat being deleted, or a deleted chat coming back.
type ChatState = chatdb.ChatState

// WatchChats calls handle for every chat already deleted, then for every chat deleted or brought
// back, until ctx ends.
func (s *Service) WatchChats(ctx context.Context, handle func(ChatState)) error {
	return chatdb.WatchChats(ctx, s.chatDB, handle)
}

// DeleteChat moves every chat of the conversation a chat is part of, and their messages, to
// Recently Deleted in Messages.
func (s *Service) DeleteChat(ctx context.Context, chatGUID string) error {
	conversation, err := s.Conversation(ctx, chatGUID)
	if err != nil {
		return err
	}
	for _, guid := range conversation.Chats {
		if err := s.client.ChatsDelete(ctx, guid); err != nil {
			return err
		}
	}
	return nil
}

// NewMessageGUID returns a GUID for SendText, for callers that must know it before sending.
func NewMessageGUID() string { return uuid.NewString() }

// SendText sends text to a chat as a new message with the given GUID from NewMessageGUID.
func (s *Service) SendText(ctx context.Context, chatGUID, guid, text, replyTo string) error {
	return s.client.SendTracked(ctx, chatGUID, guid, text, replyTo)
}

// Chat looks up a chat by GUID.
func (s *Service) Chat(ctx context.Context, guid string) (Chat, error) {
	s.lock.Lock()
	chat, ok := s.chats[guid]
	id, known := s.chatIDs[guid]
	s.lock.Unlock()
	if ok {
		return chat, nil
	}
	if !known {
		if err := s.refreshChats(ctx); err != nil {
			return Chat{}, err
		}
		s.lock.Lock()
		id, known = s.chatIDs[guid]
		s.lock.Unlock()
		if !known {
			return Chat{}, fmt.Errorf("no chat %s", guid)
		}
	}
	c, err := s.client.ChatsGet(ctx, id)
	if err != nil {
		return Chat{}, err
	}
	chat = toChat(c)
	s.lock.Lock()
	s.chats[guid] = chat
	s.lock.Unlock()
	return chat, nil
}

// allChats is more than this account will have, so chats.list returns every chat.
const allChats = 100000

// RecentChats returns the chats with a message since the given time, newest first.
func (s *Service) RecentChats(ctx context.Context, since time.Time) ([]Chat, error) {
	chats, err := s.client.ChatsList(ctx, allChats, false)
	if err != nil {
		return nil, err
	}
	var recent []Chat
	s.lock.Lock()
	defer s.lock.Unlock()
	for _, c := range chats {
		s.learn(c)
		if c.LastMessage.After(since) {
			recent = append(recent, toChat(c))
		}
	}
	return recent, nil
}

// UnreadChats returns the chats with unread messages, newest first.
func (s *Service) UnreadChats(ctx context.Context) ([]Chat, error) {
	chats, err := s.client.ChatsList(ctx, allChats, true)
	if err != nil {
		return nil, err
	}
	unread := make([]Chat, 0, len(chats))
	s.lock.Lock()
	defer s.lock.Unlock()
	for _, c := range chats {
		s.learn(c)
		unread = append(unread, toChat(c))
	}
	return unread, nil
}

func (s *Service) refreshChats(ctx context.Context) error {
	chats, err := s.client.ChatsList(ctx, allChats, false)
	if err != nil {
		return err
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	for _, c := range chats {
		s.learn(c)
	}
	return nil
}

// learn records what chats.list says about a chat; the caller holds s.lock.
func (s *Service) learn(c imsg.Chat) {
	s.chatIDs[c.GUID] = c.ID
	if !c.IsGroup && c.ContactName != "" && len(c.Participants) == 1 {
		s.names[c.Participants[0]] = c.ContactName
	}
}

// Conversation is every chat with a conversation's other side that still has a message; see
// ConversationID.
type Conversation struct {
	ID string
	// Chats holds the GUIDs of its chats, the one with the newest message first.
	Chats []string
}

// Conversation looks up the conversation a chat is part of, by the chat's GUID or the
// conversation's ID. A conversation whose every chat is deleted has no chats.
func (s *Service) Conversation(ctx context.Context, id string) (Conversation, error) {
	members, err := s.members(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	conversation := Conversation{ID: ConversationID(id)}
	for _, m := range members {
		if m.Live {
			conversation.Chats = append(conversation.Chats, m.GUID)
		}
	}
	return conversation, nil
}

// Target is the chat of a conversation that sends go to: the one Messages' filter left alone, as
// a reply from the conversation list goes, or else the one with the newest message.
func (s *Service) Target(ctx context.Context, id string) (string, error) {
	conversation, err := s.Conversation(ctx, id)
	if err != nil {
		return "", err
	}
	if len(conversation.Chats) == 0 {
		return "", fmt.Errorf("conversation %s has no chat left in Messages", conversation.ID)
	}
	if slices.Contains(conversation.Chats, conversation.ID) {
		return conversation.ID, nil
	}
	return conversation.Chats[0], nil
}

// members returns the chats of the conversation a chat is part of, the one with the newest
// message first.
func (s *Service) members(ctx context.Context, id string) ([]chatdb.Member, error) {
	members, err := chatdb.Members(ctx, s.chatDB, ConversationID(id))
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("no chat %s", id)
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	for _, m := range members {
		s.chatIDs[m.GUID] = m.RowID
	}
	return members, nil
}

// History returns up to limit of the messages of the conversation a chat is part of, older than
// before, or newer than after, oldest first. Tapbacks are left out.
func (s *Service) History(ctx context.Context, chatGUID string, after, before time.Time, limit int) ([]Message, error) {
	members, err := s.members(ctx, chatGUID)
	if err != nil {
		return nil, err
	}
	if !after.IsZero() {
		// imsg's start bound is inclusive.
		after = after.Add(time.Nanosecond)
	}
	var found []imsg.Message
	for _, m := range members {
		page, err := s.client.History(ctx, m.RowID, limit, after, before, true)
		if err != nil {
			return nil, err
		}
		found = append(found, page...)
	}
	// imsg pages each chat newest first; keep the newest limit of them all.
	slices.SortFunc(found, func(a, b imsg.Message) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	found = found[:min(limit, len(found))]
	messages := make([]Message, 0, len(found))
	for _, f := range slices.Backward(found) {
		messages = append(messages, toMessage(f))
	}
	s.announce(ctx, messages)
	return messages, nil
}

// SendFile sends a file to a chat, as a voice message when audio is set, and returns the new
// message's GUID.
func (s *Service) SendFile(ctx context.Context, chatGUID, path, replyTo string, audio bool) (string, error) {
	return s.client.SendAttachment(ctx, chatGUID, path, replyTo, audio)
}

// SetTapback adds or removes this account's tapback, given as an emoji, on a message.
func (s *Service) SetTapback(ctx context.Context, chatGUID, target, emoji string, remove bool) error {
	return s.client.Tapback(ctx, chatGUID, target, tapbackReaction(emoji), remove)
}

// Edit replaces the text of a message this account sent.
func (s *Service) Edit(ctx context.Context, chatGUID, guid, text string) error {
	return s.client.Edit(ctx, chatGUID, guid, text)
}

// Unsend retracts a message this account sent.
func (s *Service) Unsend(ctx context.Context, chatGUID, guid string) error {
	return s.client.Unsend(ctx, chatGUID, guid)
}

// Name is the contact's name for a handle, or the handle itself for someone not in Contacts.
func (s *Service) Name(handle string) string {
	if name, ok := s.contacts.Name(handle); ok {
		return name
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	if name, ok := s.names[handle]; ok {
		return name
	}
	return handle
}

// ContactName is the name of the one contact a handle belongs to.
func (s *Service) ContactName(handle string) (string, bool) {
	return s.contacts.Name(handle)
}

// Photo is the picture of the contact a handle belongs to, or nil when it has none.
func (s *Service) Photo(handle string) []byte {
	contact, _ := s.contacts.Lookup(handle)
	return contact.Photo
}

// Person finds the one contact a name means; see contacts.Catalog.Resolve.
func (s *Service) Person(name string) (contacts.Contact, error) {
	return s.contacts.Resolve(name)
}

// Search finds messages; see search.Backend.
func (s *Service) Search(ctx context.Context, q search.Query) (search.Results, error) {
	return s.search.Search(ctx, q)
}

// MessageChat returns the GUID of the chat a message is in, and the message's files.
func (s *Service) MessageChat(ctx context.Context, guid string) (string, []chatdb.File, error) {
	return chatdb.MessageChat(ctx, s.chatDB, guid)
}

// StartChat starts a chat with one address by sending it text, and returns the chat's GUID and
// the message's.
func (s *Service) StartChat(ctx context.Context, address, text string) (chatGUID, messageGUID string, err error) {
	return s.client.ChatsCreate(ctx, []string{address}, text)
}

// MarkRead marks every message in the conversation a chat is part of read.
func (s *Service) MarkRead(ctx context.Context, chatGUID string) error {
	conversation, err := s.Conversation(ctx, chatGUID)
	if err != nil {
		return err
	}
	for _, guid := range conversation.Chats {
		if err := s.client.Read(ctx, guid); err != nil {
			return err
		}
	}
	return nil
}

// readCheckDepth is how far back MarkReadThrough looks for the receipted message.
const readCheckDepth = 50

// MarkReadThrough marks the conversation a chat is part of read for a reader who has seen
// everything up to the message with the given GUID. imsg can only mark a whole chat read, so it
// leaves alone each of the conversation's chats with a newer message from someone else that the
// reader hasn't seen, and reports whether it marked any.
func (s *Service) MarkReadThrough(ctx context.Context, chatGUID, guid string) (bool, error) {
	members, err := s.members(ctx, chatGUID)
	if err != nil {
		return false, err
	}
	// Row IDs order messages across every chat on this Mac.
	seen := int64(-1)
	newestInbound := make([]int64, len(members))
	for i, member := range members {
		messages, err := s.client.History(ctx, member.RowID, readCheckDepth, time.Time{}, time.Time{}, false)
		if err != nil {
			return false, err
		}
		for _, m := range messages {
			if m.GUID == guid {
				seen = m.ID
			}
			if !m.IsFromMe {
				newestInbound[i] = max(newestInbound[i], m.ID)
			}
		}
	}
	if seen < 0 {
		return false, nil
	}
	marked := false
	for i, member := range members {
		if !member.Live || newestInbound[i] > seen {
			continue
		}
		if err := s.client.Read(ctx, member.GUID); err != nil {
			return marked, err
		}
		marked = true
	}
	return marked, nil
}

func (s *Service) notify(method string, params json.RawMessage) {
	switch method {
	case imsg.NotifyMessage, imsg.NotifyOverflow, imsg.NotifyError,
		imsg.NotifyBridgeEvent, imsg.NotifyBridgeOverflow, imsg.NotifyBridgeError:
	default:
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

// remember records what a watched row says about its chat and sender, and returns the chat as it
// is now when its name or members differ from what was known.
func (s *Service) remember(m imsg.Message, seen map[string]Chat) (ChatChange, bool) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.chatIDs[m.ChatGUID] = m.ChatID
	if !m.IsFromMe && m.SenderName != "" {
		s.names[m.Sender] = m.SenderName
	}
	if !m.IsGroup {
		return ChatChange{}, false
	}
	known, ok := seen[m.ChatGUID]
	name := m.ChatName
	if name == m.ChatIdent {
		// imsg names an unnamed chat by its identifier.
		name = ""
	}
	now := Chat{GUID: m.ChatGUID, Name: strings.TrimSpace(name), IsGroup: true, Participants: m.Participants, LastMessage: m.CreatedAt}
	s.chats[m.ChatGUID] = now
	seen[m.ChatGUID] = now
	if !ok || (known.Name == now.Name && slices.Equal(known.Participants, now.Participants)) {
		return ChatChange{}, false
	}
	return ChatChange{Chat: now}, true
}
