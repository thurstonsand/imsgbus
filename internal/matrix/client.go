package matrix

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

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
}

var (
	_ bridgev2.NetworkAPI                    = (*Client)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*Client)(nil)
	_ bridgev2.TypingHandlingNetworkAPI      = (*Client)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI    = (*Client)(nil)
	_ bridgev2.EditHandlingNetworkAPI        = (*Client)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI   = (*Client)(nil)
	_ bridgev2.BackfillingNetworkAPI         = (*Client)(nil)
)

const watchRetryDelay = 5 * time.Second

// recentChats is how far back a connect looks for chats to bring into Beeper.
const recentChats = 30 * 24 * time.Hour

func (c *Client) Connect(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	c.lock.Lock()
	c.cancel = cancel
	c.lock.Unlock()
	c.login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	go c.syncChats(ctx)
	go c.keep(ctx, "message watch", func() error {
		err := c.core.Watch(ctx, c.cursor.position(), c.handle)
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
			log.Warn().Msg("Watch fell behind; resuming")
			continue
		}
		log.Err(err).Msg("Watch ended; retrying")
		select {
		case <-ctx.Done():
			return
		case <-time.After(watchRetryDelay):
		}
	}
}

// syncChats brings recently active chats into Beeper and refreshes their info. Only a new portal
// backfills: the message watch replays what existing ones missed.
func (c *Client) syncChats(ctx context.Context) {
	chats, err := c.core.RecentChats(ctx, time.Now().Add(-recentChats))
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to list recent chats")
		return
	}
	for _, chat := range chats {
		c.login.QueueRemoteEvent(&simplevent.ChatResync{
			Type:            bridgev2.RemoteEventChatResync,
			PortalKey:       c.portalKey(chat.GUID),
			CreatePortal:    true,
			GetChatInfoFunc: c.GetChatInfo,
			LatestMessageTS: chat.LastMessage,
			CheckNeedsBackfillFunc: func(_ context.Context, latest *database.Message) (bool, error) {
				return latest == nil, nil
			},
		})
	}
}

func (c *Client) handle(evt core.Event) {
	switch evt := evt.(type) {
	case core.Message:
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
	zerolog.Ctx(ctx).Debug().Str("guid", r.GUID).Stringer("type", evtType).Bool("from_me", r.IsFromMe).Msg("Bridging receipt")
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
		}
	}
	return info
}

func (c *Client) GetUserInfo(_ context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	handle := string(ghost.ID)
	name := c.core.Name(handle)
	identifier := "tel:" + handle
	if strings.Contains(handle, "@") {
		identifier = "mailto:" + handle
	}
	return &bridgev2.UserInfo{Name: &name, Identifiers: []string{identifier}}, nil
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
		ID: "house.thurstons.imsgbus.capabilities.v3",
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
	}
}
