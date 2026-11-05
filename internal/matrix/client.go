package matrix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/nyaruka/phonenumbers"
	"github.com/rs/zerolog"
	"go.mau.fi/util/jsontime"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	"github.com/thurstonsand/imsgbus/internal/core"
)

// Client bridges one login's messages between core and its Matrix portals.
type Client struct {
	login  *bridgev2.UserLogin
	core   *core.Service
	cursor *cursor

	lock   sync.Mutex
	cancel context.CancelFunc
	// deleting holds the chats Beeper is deleting, whose deletion in Messages the chat watch
	// would otherwise bring back to delete again.
	deleting map[string]bool
	sends    sends
}

var (
	_ bridgev2.NetworkAPI                    = (*Client)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*Client)(nil)
	_ bridgev2.TypingHandlingNetworkAPI      = (*Client)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI    = (*Client)(nil)
	_ bridgev2.EditHandlingNetworkAPI        = (*Client)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI   = (*Client)(nil)
	_ bridgev2.BackfillingNetworkAPI         = (*Client)(nil)
	_ bridgev2.DeleteChatHandlingNetworkAPI  = (*Client)(nil)
)

const watchRetryDelay = 5 * time.Second

func (c *Client) Connect(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	c.lock.Lock()
	c.cancel = cancel
	c.lock.Unlock()
	c.login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	go c.syncChats(ctx)
	go c.keep(ctx, "message watch", func() error {
		err := c.core.Watch(ctx, c.cursor.position(), true, func(evt core.Event) { c.handle(ctx, evt) })
		if _, ok := errors.AsType[*core.OverflowError](err); ok {
			// The cursor is still before every row the watch didn't finish, so resume from it.
			return nil
		}
		return err
	})
	go c.keep(ctx, "receipt watch", func() error {
		return c.core.WatchReceipts(ctx, func(r core.Receipt) { c.handleReceipt(ctx, r) })
	})
	go c.keep(ctx, "typing watch", func() error {
		return c.core.WatchTyping(ctx, func(t core.Typing) { c.handleTyping(t) })
	})
	go c.keep(ctx, "chat watch", func() error {
		return c.core.WatchChats(ctx, func(s core.ChatState) { c.handleChatState(ctx, s) })
	})
}

func (c *Client) Disconnect() {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
}

// keep runs a watch until ctx ends, restarting it when it ends early.
func (c *Client) keep(ctx context.Context, name string, run func() error) {
	log := zerolog.Ctx(ctx).With().Str("watch", name).Logger()
	for {
		err := run()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			log.Warn().Str("event", "watch_restart").Msg("Watch fell behind; resuming")
			continue
		}
		log.Err(err).Str("event", "watch_restart").Msg("Watch ended; retrying")
		select {
		case <-ctx.Done():
			return
		case <-time.After(watchRetryDelay):
		}
	}
}

// syncChats brings every chat with a message into Beeper and refreshes its info. Only a new portal
// backfills: the message watch replays what existing ones missed.
func (c *Client) syncChats(ctx context.Context) {
	chats, err := c.core.RecentChats(ctx, time.Time{})
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to list chats")
		return
	}
	for _, chat := range chats {
		c.bringIn(chat.GUID, chat.LastMessage)
	}
}

// bringIn creates a chat's portal if it has none, with its history, and refreshes its info.
func (c *Client) bringIn(chatGUID string, lastMessage time.Time) {
	c.login.QueueRemoteEvent(&simplevent.ChatResync{
		Type:            bridgev2.RemoteEventChatResync,
		PortalKey:       c.portalKey(chatGUID),
		CreatePortal:    true,
		GetChatInfoFunc: c.GetChatInfo,
		LatestMessageTS: lastMessage,
		CheckNeedsBackfillFunc: func(_ context.Context, latest *database.Message) (bool, error) {
			return latest == nil, nil
		},
	})
}

// handleChatState deletes a deleted chat's portal, and brings back a chat recovered from Recently
// Deleted, with the history it got back.
func (c *Client) handleChatState(ctx context.Context, s core.ChatState) {
	log := zerolog.Ctx(ctx).With().Str("event", "chat_state").Str("chat_guid", s.GUID).Bool("deleted", s.Deleted).Logger()
	if !s.Deleted {
		log.Info().Msg("Bringing in a chat recovered in iMessage, or new to it")
		c.bringIn(s.GUID, time.Time{})
		return
	}
	c.lock.Lock()
	ours := c.deleting[s.GUID]
	delete(c.deleting, s.GUID)
	c.lock.Unlock()
	if ours {
		return
	}
	// Looking a portal up by key creates it, so only an existing room is deleted.
	portal, err := c.login.Bridge.GetExistingPortalByKey(ctx, c.portalKey(s.GUID))
	if err != nil {
		log.Err(err).Msg("Failed to look up the portal of a deleted chat")
		return
	}
	if portal == nil || portal.MXID == "" {
		return
	}
	log.Info().Msg("Deleting a chat deleted in iMessage")
	c.login.QueueRemoteEvent(&simplevent.ChatDelete{
		Type:      bridgev2.RemoteEventChatDelete,
		PortalKey: portal.PortalKey,
		Timestamp: time.Now(),
		OnlyForMe: true,
	})
}

func (c *Client) handle(ctx context.Context, evt core.Event) {
	c.logInbound(evt)
	switch evt := evt.(type) {
	case core.Message:
		if evt.IsFromMe {
			if sent := c.sends.echoOf(evt.ChatGUID, evt.GUID, evt.Text, evt.Time); sent != "" {
				c.adoptGUID(ctx, evt.ChatGUID, sent, evt.GUID)
				return
			}
		}
		c.cursor.start(evt.RowID)
		c.login.QueueRemoteEvent(&simplevent.Message[core.Message]{
			Type:         bridgev2.RemoteEventMessage,
			PortalKey:    c.portalKey(evt.ChatGUID),
			CreatePortal: true,
			Sender:       c.sender(evt.IsFromMe, evt.Sender),
			Timestamp:    evt.Time,
			StreamOrder:  evt.RowID,
			PostHandleFunc: func(ctx context.Context, _ *bridgev2.Portal) {
				c.cursor.finish(ctx, evt.RowID)
			},
			ID:                 networkid.MessageID(evt.GUID),
			TransactionID:      networkid.TransactionID(evt.GUID),
			Data:               evt,
			ConvertMessageFunc: convertMessage,
		})
	case core.Tapback:
		c.cursor.start(evt.RowID)
		evtType := bridgev2.RemoteEventReaction
		if evt.Removed {
			evtType = bridgev2.RemoteEventReactionRemove
		}
		c.login.QueueRemoteEvent(&simplevent.Reaction{
			Type:      evtType,
			PortalKey: c.portalKey(evt.ChatGUID),
			Sender:    c.sender(evt.IsFromMe, evt.Sender),
			Timestamp: evt.Time,
			PostHandleFunc: func(ctx context.Context, _ *bridgev2.Portal) {
				c.cursor.finish(ctx, evt.RowID)
			},
			TargetMessage: networkid.MessageID(evt.Target),
			Emoji:         evt.Emoji,
		})
	case core.Edit:
		c.login.QueueRemoteEvent(&simplevent.Message[core.Edit]{
			Type:            bridgev2.RemoteEventEdit,
			PortalKey:       c.portalKey(evt.ChatGUID),
			Sender:          c.sender(evt.IsFromMe, evt.Sender),
			Timestamp:       evt.Time,
			TargetMessage:   networkid.MessageID(evt.GUID),
			Data:            evt,
			ConvertEditFunc: convertEdit,
		})
	case core.Unsend:
		c.login.QueueRemoteEvent(&simplevent.MessageRemove{
			Type:          bridgev2.RemoteEventMessageRemove,
			PortalKey:     c.portalKey(evt.ChatGUID),
			Sender:        c.sender(evt.IsFromMe, evt.Sender),
			Timestamp:     evt.Time,
			TargetMessage: networkid.MessageID(evt.GUID),
		})
	case core.ChatChange:
		c.login.QueueRemoteEvent(&simplevent.ChatResync{
			Type:      bridgev2.RemoteEventChatResync,
			PortalKey: c.portalKey(evt.Chat.GUID),
			ChatInfo:  c.chatInfo(evt.Chat),
		})
	}
}

// logInbound records each event the watch hands to Beeper, with how long after it happened.
// adoptGUID points the message Beeper sent as sent at guid, which Messages recorded as guid,
// so receipts, tapbacks and edits on it find the event Beeper already shows.
func (c *Client) adoptGUID(ctx context.Context, chatGUID, sent, guid string) {
	log := zerolog.Ctx(ctx).With().Str("chat_guid", chatGUID).Str("sent_guid", sent).Str("guid", guid).Logger()
	msg, err := c.login.Bridge.DB.Message.GetPartByID(ctx, c.portalKey(chatGUID).Receiver, networkid.MessageID(sent), "")
	if err != nil {
		log.Err(err).Msg("Dropped Messages' copy of a send from Beeper, but failed to look up the message")
		return
	}
	if msg == nil {
		// A file's caption is never saved as a message of its own.
		log.Info().Msg("Dropped Messages' copy of a send from Beeper recorded under another GUID")
		return
	}
	msg.ID = networkid.MessageID(guid)
	if err = c.login.Bridge.DB.Message.Update(ctx, msg); err != nil {
		log.Err(err).Msg("Dropped Messages' copy of a send from Beeper, but failed to point the message at it")
		return
	}
	log.Info().Msg("Matched Messages' copy of a send from Beeper recorded under another GUID")
}

func (c *Client) logInbound(evt core.Event) {
	log := c.login.Log.Info().Str("event", "inbound")
	var at time.Time
	switch evt := evt.(type) {
	case core.Message:
		log = log.Str("kind", "message").Str("chat_guid", evt.ChatGUID).Str("guid", evt.GUID).Str("sender", evt.Sender).Bool("from_me", evt.IsFromMe).
			Bool("group", evt.IsGroup).Int("files", len(evt.Attachments)).Str("app", evt.App)
		at = evt.Time
	case core.Tapback:
		log = log.Str("kind", "tapback").Str("chat_guid", evt.ChatGUID).Str("guid", evt.GUID).Str("target", evt.Target).Str("sender", evt.Sender).Bool("from_me", evt.IsFromMe)
		at = evt.Time
	case core.Edit:
		log = log.Str("kind", "edit").Str("chat_guid", evt.ChatGUID).Str("guid", evt.GUID).Str("sender", evt.Sender).Bool("from_me", evt.IsFromMe)
		at = evt.Time
	case core.Unsend:
		log = log.Str("kind", "unsend").Str("chat_guid", evt.ChatGUID).Str("guid", evt.GUID).Str("sender", evt.Sender).Bool("from_me", evt.IsFromMe)
		at = evt.Time
	case core.ChatChange:
		log = log.Str("kind", "chat_change").Str("chat_guid", evt.Chat.GUID)
	}
	if !at.IsZero() {
		log = log.Int64("lag_ms", time.Since(at).Milliseconds())
	}
	log.Msg("Bridging event from iMessage")
}

func (c *Client) handleReceipt(ctx context.Context, r core.Receipt) {
	sender := c.self()
	if r.IsFromMe {
		other, ok := otherHandle(r.ChatGUID)
		if !ok {
			// Groups report delivery and reads for no one in particular.
			return
		}
		sender = bridgev2.EventSender{Sender: networkid.UserID(other)}
	}
	evtType := bridgev2.RemoteEventReadReceipt
	if r.Kind == core.Delivered {
		evtType = bridgev2.RemoteEventDeliveryReceipt
	}
	zerolog.Ctx(ctx).Info().Str("event", "receipt").Str("chat_guid", r.ChatGUID).Str("guid", r.GUID).Stringer("type", evtType).Bool("from_me", r.IsFromMe).
		Int64("lag_ms", time.Since(r.Time).Milliseconds()).Msg("Bridging receipt from iMessage")
	c.login.QueueRemoteEvent(&simplevent.Receipt{
		Type:                evtType,
		PortalKey:           c.portalKey(r.ChatGUID),
		Sender:              sender,
		Timestamp:           r.Time,
		LastTarget:          networkid.MessageID(r.GUID),
		Targets:             []networkid.MessageID{networkid.MessageID(r.GUID)},
		ReadUpTo:            r.Time,
		ReadUpToStreamOrder: r.RowID,
	})
}

// typingTimeout is how long Beeper shows someone typing without hearing they stopped.
const typingTimeout = 60 * time.Second

func (c *Client) handleTyping(t core.Typing) {
	other, ok := otherHandle(t.ChatGUID)
	if !ok {
		return
	}
	timeout := time.Duration(0)
	if t.IsTyping {
		timeout = typingTimeout
	}
	c.login.QueueRemoteEvent(&simplevent.Typing{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventTyping,
			PortalKey: c.portalKey(t.ChatGUID),
			Sender:    bridgev2.EventSender{Sender: networkid.UserID(other)},
		},
		Timeout: timeout,
	})
}

// otherHandle is the other side of a one-on-one chat, from its GUID ("any;-;+15551234567").
func otherHandle(chatGUID string) (string, bool) {
	_, after, ok := strings.Cut(chatGUID, ";-;")
	return after, ok
}

func (c *Client) portalKey(chatGUID string) networkid.PortalKey {
	return networkid.PortalKey{ID: networkid.PortalID(chatGUID), Receiver: c.login.ID}
}

func (c *Client) sender(isFromMe bool, handle string) bridgev2.EventSender {
	if isFromMe {
		return c.self()
	}
	return bridgev2.EventSender{Sender: networkid.UserID(handle)}
}

func (c *Client) self() bridgev2.EventSender {
	return bridgev2.EventSender{IsFromMe: true, SenderLogin: c.login.ID, Sender: networkid.UserID(c.login.RemoteName)}
}

func (c *Client) IsLoggedIn() bool { return true }

func (c *Client) LogoutRemote(context.Context) {}

func (c *Client) IsThisUser(_ context.Context, userID networkid.UserID) bool {
	return string(userID) == c.login.RemoteName
}

func (c *Client) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	chat, err := c.core.Chat(ctx, string(portal.ID))
	if err != nil {
		return nil, err
	}
	return c.chatInfo(chat), nil
}

func (c *Client) chatInfo(chat core.Chat) *bridgev2.ChatInfo {
	members := &bridgev2.ChatMemberList{
		IsFull:    true,
		MemberMap: bridgev2.ChatMemberMap{},
	}
	members.MemberMap.Set(bridgev2.ChatMember{EventSender: c.self()})
	for _, handle := range chat.Participants {
		members.MemberMap.Set(bridgev2.ChatMember{Sender: networkid.UserID(handle)})
	}
	info := &bridgev2.ChatInfo{Members: members, CanBackfill: true}
	if chat.IsGroup {
		info.Type = new(database.RoomTypeDefault)
		if chat.Name != "" {
			info.Name = &chat.Name
		}
	} else {
		info.Type = new(database.RoomTypeDM)
		if len(chat.Participants) == 1 {
			members.OtherUserID = networkid.UserID(chat.Participants[0])
			// Messages files these under Transactions, Promotions or Unknown Senders.
			if _, category := core.Address(chat.Participants[0]); category != "" {
				info.UserLocal = &bridgev2.UserLocalPortalInfo{Tag: new(event.RoomTagLowPriority)}
			}
		}
	}
	return info
}

func (c *Client) GetUserInfo(_ context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	return userInfo(c.core, ghost), nil
}

// userInfo shows a handle as its contact, with their photo, or as its address written out.
func userInfo(service *core.Service, ghost *bridgev2.Ghost) *bridgev2.UserInfo {
	handle := string(ghost.ID)
	address, _ := core.Address(handle)
	name := service.Name(handle)
	if name == handle {
		name = displayAddress(address)
	}
	identifier := "tel:" + address
	if strings.Contains(address, "@") {
		identifier = "mailto:" + address
	}
	info := &bridgev2.UserInfo{Name: &name, Identifiers: []string{identifier}}
	if photo := service.Photo(handle); photo != nil {
		sum := sha256.Sum256(photo)
		info.Avatar = &bridgev2.Avatar{
			ID:  networkid.AvatarID(hex.EncodeToString(sum[:8])),
			Get: func(context.Context) ([]byte, error) { return photo, nil },
		}
	} else if ghost.AvatarID != "" {
		info.Avatar = &bridgev2.Avatar{Remove: true}
	}
	return info
}

// displayAddress writes a phone number the way it reads internationally, as "+1 555-201-3344";
// anything else, such as an email or a short code, stays as it is.
func displayAddress(address string) string {
	number, err := phonenumbers.Parse(address, "US")
	if err != nil || !phonenumbers.IsValidNumber(number) {
		return address
	}
	return phonenumbers.Format(number, phonenumbers.INTERNATIONAL)
}

// Apple lets a sender edit a message for 15 minutes and unsend it for 2.
var (
	editWindow   = 15 * time.Minute
	unsendWindow = 2 * time.Minute
)

func (c *Client) GetCapabilities(context.Context, *bridgev2.Portal) *event.RoomFeatures {
	file := &event.FileFeatures{
		MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported},
		MaxSize:   100 << 20,
		Caption:   event.CapLevelPartialSupport,
	}
	return &event.RoomFeatures{
		ID: "house.thurstons.imsgbus.capabilities.v4",
		File: event.FileFeatureMap{
			event.MsgImage:    file,
			event.MsgVideo:    file,
			event.MsgAudio:    file,
			event.MsgFile:     file,
			event.CapMsgVoice: file,
		},
		Reply:               event.CapLevelFullySupported,
		Edit:                event.CapLevelFullySupported,
		EditMaxAge:          new(jsontime.S(editWindow)),
		Delete:              event.CapLevelFullySupported,
		DeleteMaxAge:        new(jsontime.S(unsendWindow)),
		Reaction:            event.CapLevelFullySupported,
		ReactionCount:       1,
		ReadReceipts:        true,
		TypingNotifications: true,
		DeleteChat:          true,
	}
}
