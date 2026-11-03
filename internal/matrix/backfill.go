package matrix

import (
	"context"
	"slices"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/thurstonsand/imsgbus/internal/core"
)

// FetchMessages pages through a chat's history by time: backwards before the cursor, or the
// oldest bridged message, and forwards after the anchor.
func (c *Client) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	var after, before time.Time
	switch {
	case params.Forward && params.AnchorMessage != nil:
		after = params.AnchorMessage.Timestamp
	case !params.Forward && params.Cursor != "":
		var err error
		if before, err = time.Parse(time.RFC3339Nano, string(params.Cursor)); err != nil {
			return nil, err
		}
	case !params.Forward && params.AnchorMessage != nil:
		before = params.AnchorMessage.Timestamp
	}
	messages, err := c.core.History(ctx, string(params.Portal.ID), after, before, params.Count)
	if err != nil {
		return nil, err
	}
	resp := &bridgev2.FetchMessagesResponse{
		Forward: params.Forward,
		HasMore: len(messages) == params.Count,
	}
	if !before.IsZero() {
		// imsg can return the message at the cursor itself, and bridgev2 fails a backwards page that
		// holds nothing older than what it already bridged.
		messages = slices.DeleteFunc(messages, func(m core.Message) bool { return !m.Time.Before(before) })
	}
	for _, msg := range messages {
		converted, err := convertMessage(ctx, params.Portal, c.login.Bridge.Bot, msg)
		if err != nil {
			return nil, err
		}
		resp.Messages = append(resp.Messages, &bridgev2.BackfillMessage{
			ConvertedMessage: converted,
			Sender:           c.sender(msg.IsFromMe, msg.Sender),
			ID:               networkid.MessageID(msg.GUID),
			TxnID:            networkid.TransactionID(msg.GUID),
			Timestamp:        msg.Time,
			StreamOrder:      msg.RowID,
		})
	}
	if len(messages) > 0 {
		resp.Cursor = networkid.PaginationCursor(messages[0].Time.Format(time.RFC3339Nano))
	}
	return resp, nil
}
