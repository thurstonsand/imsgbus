package matrix

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
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
)

const watchRetryDelay = 5 * time.Second

func (c *Client) Connect(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	c.lock.Lock()
	c.cancel = cancel
	c.lock.Unlock()
	c.login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
	go c.watch(ctx)
}

func (c *Client) Disconnect() {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *Client) watch(ctx context.Context) {
	log := zerolog.Ctx(ctx)
	for {
		err := c.core.Watch(ctx, c.cursor.position(), func(msg core.Message) { c.handle(ctx, msg) })
		if ctx.Err() != nil {
			return
		}
		if overflow, ok := errors.AsType[*core.OverflowError](err); ok {
			log.Warn().Int64("resume_after", overflow.ResumeAfter).Msg("Watch fell behind; resuming")
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

func (c *Client) handle(ctx context.Context, msg core.Message) {
	if msg.IsGroup {
		c.cursor.finish(ctx, msg.RowID)
		return
	}
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: msg.Text}
	if msg.Text == "" {
		// A row without text, such as an attachment, still lands in the portal: a read receipt
		// on what Beeper shows as newest must not mark an unseen newer row read.
		content = &event.MessageEventContent{MsgType: event.MsgNotice, Body: "Unsupported message; open it on an Apple device"}
	}
	c.cursor.start(msg.RowID)
	c.login.QueueRemoteEvent(&simplevent.PreConvertedMessage{
		Type:         bridgev2.RemoteEventMessage,
		PortalKey:    c.portalKey(msg.ChatGUID),
		CreatePortal: true,
		Sender:       c.sender(msg),
		Timestamp:    msg.Time,
		StreamOrder:  msg.RowID,
		PostHandleFunc: func(ctx context.Context, _ *bridgev2.Portal) {
			c.cursor.finish(ctx, msg.RowID)
		},
		ID:            networkid.MessageID(msg.GUID),
		TransactionID: networkid.TransactionID(msg.GUID),
		Data: &bridgev2.ConvertedMessage{
			ReplyTo: replyTo(msg.ReplyTo),
			Parts:   []*bridgev2.ConvertedMessagePart{{Type: event.EventMessage, Content: content}},
		},
	})
}

func (c *Client) portalKey(chatGUID string) networkid.PortalKey {
	return networkid.PortalKey{ID: networkid.PortalID(chatGUID), Receiver: c.login.ID}
}

func (c *Client) sender(msg core.Message) bridgev2.EventSender {
	if msg.IsFromMe {
		return c.self()
	}
	return bridgev2.EventSender{Sender: networkid.UserID(msg.Sender)}
}

func (c *Client) self() bridgev2.EventSender {
	return bridgev2.EventSender{IsFromMe: true, SenderLogin: c.login.ID, Sender: networkid.UserID(c.login.RemoteName)}
}

func replyTo(guid string) *networkid.MessageOptionalPartID {
	if guid == "" {
		return nil
	}
	return &networkid.MessageOptionalPartID{MessageID: networkid.MessageID(guid)}
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
	members := &bridgev2.ChatMemberList{
		IsFull:    true,
		MemberMap: bridgev2.ChatMemberMap{},
	}
	members.MemberMap.Set(bridgev2.ChatMember{EventSender: c.self()})
	for _, handle := range chat.Participants {
		members.MemberMap.Set(bridgev2.ChatMember{Sender: networkid.UserID(handle)})
	}
	info := &bridgev2.ChatInfo{Members: members}
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
	return info, nil
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

func (c *Client) GetCapabilities(context.Context, *bridgev2.Portal) *event.RoomFeatures {
	return &event.RoomFeatures{
		ID:           "house.thurstons.imsgbus.capabilities.v1",
		Reply:        event.CapLevelFullySupported,
		ReadReceipts: true,
	}
}

func (c *Client) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	if msg.Content.MsgType != event.MsgText {
		return nil, bridgev2.ErrUnsupportedMessageType
	}
	var replyGUID string
	if msg.ReplyTo != nil {
		replyGUID = string(msg.ReplyTo.ID)
	}
	guid := core.NewMessageGUID()
	txn := networkid.TransactionID(guid)
	// The watch can deliver this message's echo before SendText returns.
	msg.AddPendingToIgnore(txn)
	if err := c.core.SendText(ctx, string(msg.Portal.ID), guid, msg.Content.Body, replyGUID); err != nil {
		return nil, err
	}
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        networkid.MessageID(guid),
			SenderID:  networkid.UserID(c.login.RemoteName),
			Timestamp: time.Now(),
		},
		RemovePending: txn,
	}, nil
}

// HandleMatrixReadReceipt marks the chat read once the receipt covers every message from the
// other side; see core.MarkReadThrough.
func (c *Client) HandleMatrixReadReceipt(ctx context.Context, receipt *bridgev2.MatrixReadReceipt) error {
	if receipt.ExactMessage == nil {
		return nil
	}
	marked, err := c.core.MarkReadThrough(ctx, string(receipt.Portal.ID), string(receipt.ExactMessage.ID))
	zerolog.Ctx(ctx).Debug().Bool("marked_read", marked).Msg("Handled read receipt")
	return err
}
