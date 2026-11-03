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
	chatGUID := string(msg.Portal.ID)
	var replyGUID string
	if msg.ReplyTo != nil {
		replyGUID = string(msg.ReplyTo.ID)
	}
	var guid string
	switch msg.Content.MsgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
		guid = core.NewMessageGUID()
		if err := c.core.SendText(ctx, chatGUID, guid, msg.Content.Body, replyGUID); err != nil {
			return nil, err
		}
	case event.MsgImage, event.MsgVideo, event.MsgAudio, event.MsgFile:
		var err error
		if guid, err = c.sendFile(ctx, msg, replyGUID); err != nil {
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

// sendFile sends a Matrix file, then its caption as a message of its own, as Messages does, and
// returns the file's message GUID.
func (c *Client) sendFile(ctx context.Context, msg *bridgev2.MatrixMessage, replyGUID string) (string, error) {
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
	guid, err := c.core.SendFile(ctx, string(msg.Portal.ID), path, replyGUID, content.MSC3245Voice != nil)
	if err != nil {
		return "", err
	}
	if caption := content.Body; content.FileName != "" && caption != content.FileName {
		captionGUID := core.NewMessageGUID()
		msg.AddPendingToIgnore(networkid.TransactionID(captionGUID))
		if err = c.core.SendText(ctx, string(msg.Portal.ID), captionGUID, caption, ""); err != nil {
			msg.RemovePending(networkid.TransactionID(captionGUID))
			zerolog.Ctx(ctx).Err(err).Msg("Sent the file but not its caption")
		}
	}
	return guid, nil
}

// HandleMatrixReadReceipt marks the chat read once the receipt covers every message from the
// other side; see core.MarkReadThrough.
func (c *Client) HandleMatrixReadReceipt(ctx context.Context, receipt *bridgev2.MatrixReadReceipt) error {
	if receipt.ExactMessage == nil {
		return nil
	}
	marked, err := c.core.MarkReadThrough(ctx, string(receipt.Portal.ID), string(receipt.ExactMessage.ID))
	zerolog.Ctx(ctx).Info().Str("event", "read_receipt").Bool("marked_read", marked).Msg("Handled read receipt from Beeper")
	return err
}

func (c *Client) HandleMatrixTyping(ctx context.Context, typing *bridgev2.MatrixTyping) error {
	return c.core.SetTyping(ctx, string(typing.Portal.ID), typing.IsTyping)
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
	err := c.core.SetTapback(ctx, string(msg.Portal.ID), string(msg.TargetMessage.ID), msg.PreHandleResp.Emoji, false)
	return &database.Reaction{}, err
}

func (c *Client) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	return c.core.SetTapback(ctx, string(msg.Portal.ID), string(msg.TargetReaction.MessageID), msg.TargetReaction.Emoji, true)
}

func (c *Client) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	return c.core.Edit(ctx, string(msg.Portal.ID), string(msg.EditTarget.ID), msg.Content.Body)
}

// HandleMatrixMessageRemove unsends a message this account sent; iMessage can't delete anyone
// else's for everyone.
func (c *Client) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	if msg.TargetMessage.SenderID != networkid.UserID(c.login.RemoteName) {
		return bridgev2.ErrUnsupportedMessageType
	}
	return c.core.Unsend(ctx, string(msg.Portal.ID), string(msg.TargetMessage.ID))
}
