package matrix

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/thurstonsand/imsgbus/internal/core"
)

func (c *Client) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	chatGUID, err := c.target(ctx, string(msg.Portal.ID))
	if err != nil {
		return nil, err
	}
	var replyGUID string
	if msg.ReplyTo != nil {
		replyGUID = string(msg.ReplyTo.ID)
	}
	var guid string
	switch msg.Content.MsgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
		guid = core.NewMessageGUID()
		c.sends.add(chatGUID, guid, msg.Content.Body, time.Now())
		if err = c.core.SendText(ctx, chatGUID, guid, msg.Content.Body, replyGUID); err != nil {
			c.sends.remove(guid)
			return nil, err
		}
	case event.MsgImage, event.MsgVideo, event.MsgAudio, event.MsgFile:
		if guid, err = c.sendFile(ctx, msg, chatGUID, replyGUID); err != nil {
			return nil, err
		}
	default:
		return nil, bridgev2.ErrUnsupportedMessageType
	}
	return &bridgev2.MatrixMessageResponse{DB: &database.Message{
		ID:        networkid.MessageID(guid),
		SenderID:  networkid.UserID(c.login.RemoteName),
		Timestamp: time.Now(),
	}}, nil
}

// sendFile sends a Matrix file to a chat, then its caption as a message of its own, as Messages
// does, and returns the file's message GUID.
func (c *Client) sendFile(ctx context.Context, msg *bridgev2.MatrixMessage, chatGUID, replyGUID string) (string, error) {
	content := msg.Content
	uri := content.URL
	if content.File != nil {
		uri = content.File.URL
	}
	data, err := c.login.Bridge.Bot.DownloadMedia(ctx, uri, content.File)
	if err != nil {
		return "", fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
	}
	dir, err := os.MkdirTemp("", "imsgbus-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	name := content.GetFileName()
	path := filepath.Join(dir, filepath.Base(name))
	if err = os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	// imsg stages its own copy for Messages, so this one can go once the send returns.
	guid, err := c.core.SendFile(ctx, chatGUID, path, replyGUID, content.MSC3245Voice != nil)
	if err != nil {
		return "", err
	}
	if caption := content.Body; content.FileName != "" && caption != content.FileName {
		captionGUID := core.NewMessageGUID()
		msg.AddPendingToIgnore(networkid.TransactionID(captionGUID))
		c.sends.add(chatGUID, captionGUID, caption, time.Now())
		if err = c.core.SendText(ctx, chatGUID, captionGUID, caption, ""); err != nil {
			msg.RemovePending(networkid.TransactionID(captionGUID))
			c.sends.remove(captionGUID)
			zerolog.Ctx(ctx).Err(err).Msg("Sent the file but not its caption")
		}
	}
	return guid, nil
}

// HandleMatrixReadReceipt marks the chat read once the receipt covers every message from the
// other side; see core.MarkReadThrough.
func (c *Client) HandleMatrixReadReceipt(ctx context.Context, receipt *bridgev2.MatrixReadReceipt) error {
	log := zerolog.Ctx(ctx).With().Str("event", "read_receipt").Str("chat_guid", string(receipt.Portal.ID)).Logger()
	read := receipt.ExactMessage
	if read == nil {
		// Beeper puts the receipt on the newest event, which can be a tapback; reading it reads
		// every message before it.
		reaction, err := c.login.Bridge.DB.Reaction.GetByMXID(ctx, receipt.EventID)
		if err != nil {
			return err
		}
		if reaction == nil {
			log.Info().Stringer("event_id", receipt.EventID).Msg("Ignored read receipt from Beeper on an event that is neither a message nor a tapback")
			return nil
		}
		if read, err = c.login.Bridge.DB.Message.GetLastNonFakePartAtOrBeforeTime(ctx, receipt.Portal.PortalKey, reaction.Timestamp); err != nil || read == nil {
			return err
		}
	}
	marked, err := c.core.MarkReadThrough(ctx, string(receipt.Portal.ID), string(read.ID))
	log.Info().Str("guid", string(read.ID)).Bool("marked_read", marked).Msg("Handled read receipt from Beeper")
	return err
}

func (c *Client) HandleMatrixTyping(ctx context.Context, typing *bridgev2.MatrixTyping) error {
	chatGUID, err := c.target(ctx, string(typing.Portal.ID))
	if err != nil {
		return err
	}
	return c.core.SetTyping(ctx, chatGUID, typing.IsTyping)
}

// PreHandleMatrixReaction keeps one tapback per person per message, as iMessage does.
func (c *Client) PreHandleMatrixReaction(_ context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	return bridgev2.MatrixReactionPreResponse{
		SenderID:     networkid.UserID(c.login.RemoteName),
		Emoji:        msg.Content.RelatesTo.Key,
		MaxReactions: 1,
	}, nil
}

func (c *Client) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	chatGUID, err := c.chatOf(ctx, msg.TargetMessage.ID)
	if err != nil {
		return nil, err
	}
	return &database.Reaction{}, c.core.SetTapback(ctx, chatGUID, string(msg.TargetMessage.ID), msg.PreHandleResp.Emoji, false)
}

func (c *Client) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	chatGUID, err := c.chatOf(ctx, msg.TargetReaction.MessageID)
	if err != nil {
		return err
	}
	return c.core.SetTapback(ctx, chatGUID, string(msg.TargetReaction.MessageID), msg.TargetReaction.Emoji, true)
}

func (c *Client) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	chatGUID, err := c.chatOf(ctx, msg.EditTarget.ID)
	if err != nil {
		return err
	}
	return c.core.Edit(ctx, chatGUID, string(msg.EditTarget.ID), msg.Content.Body)
}

// chatOf is the chat a message is in, of the several a conversation can have.
func (c *Client) chatOf(ctx context.Context, guid networkid.MessageID) (string, error) {
	chatGUID, _, err := c.core.MessageChat(ctx, string(guid))
	return chatGUID, err
}

// HandleMatrixMessageRemove unsends a message this account sent; iMessage can't delete anyone
// else's for everyone.
func (c *Client) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	if msg.TargetMessage.SenderID != networkid.UserID(c.login.RemoteName) {
		return bridgev2.ErrUnsupportedMessageType
	}
	chatGUID, err := c.chatOf(ctx, msg.TargetMessage.ID)
	if err != nil {
		return err
	}
	return c.core.Unsend(ctx, chatGUID, string(msg.TargetMessage.ID))
}

// HandleMatrixDeleteChat deletes every chat of the conversation in Messages, for this account
// only: iMessage can't delete a chat for anyone else.
func (c *Client) HandleMatrixDeleteChat(ctx context.Context, msg *bridgev2.MatrixDeleteChat) error {
	if msg.Content.DeleteForEveryone {
		return bridgev2.ErrDeleteChatNotSupported
	}
	guid := string(msg.Portal.ID)
	c.lock.Lock()
	c.deleting[guid] = true
	c.lock.Unlock()
	err := c.core.DeleteChat(ctx, guid)
	if err != nil {
		c.lock.Lock()
		delete(c.deleting, guid)
		c.lock.Unlock()
	}
	zerolog.Ctx(ctx).Info().Str("event", "chat_delete").Str("chat_guid", guid).Err(err).Msg("Deleted chat from Beeper in iMessage")
	return err
}
