package matrix

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/thurstonsand/imsgbus/internal/core"
)

// describePlaceholders edits the group changes backfilled before the bridge could describe them,
// which Beeper still shows as unsupported, into what it sends for them now. It is a one-time
// repair: every such message was backfilled, so it is the unedited backfilled ones it picks, and
// it goes once it has run, since a group change backfilled later is already described.
func (c *Client) describePlaceholders(ctx context.Context) error {
	type placeholder struct {
		guid   networkid.MessageID
		room   networkid.PortalID
		sender id.UserID
	}
	rows, err := c.login.Bridge.DB.Query(ctx, `
		SELECT id, room_id, sender_mxid FROM message
		WHERE bridge_id = $1 AND room_receiver = $2 AND part_id = $3 AND edit_count = 0 AND mxid NOT LIKE '%:' || $4`,
		c.login.Bridge.ID, c.login.ID, textPart, c.login.Bridge.Bot.GetMXID().Homeserver())
	backfilled, err := dbutil.NewRowIterWithError(rows, func(row dbutil.Scannable) (p placeholder, err error) {
		return p, row.Scan(&p.guid, &p.room, &p.sender)
	}, err).AsList()
	if err != nil {
		return err
	}
	queued := 0
	for start := 0; start < len(backfilled); start += 1000 {
		batch := backfilled[start:min(start+1000, len(backfilled))]
		guids := make([]string, len(batch))
		for i, p := range batch {
			guids[i] = string(p.guid)
		}
		announced := c.core.Announced(ctx, guids)
		for _, p := range batch {
			m, ok := announced[string(p.guid)]
			if !ok {
				continue
			}
			content := placeholderEdit(m, p.sender == c.login.Bridge.Bot.GetMXID(), c.login.RemoteProfile.Name)
			c.login.QueueRemoteEvent(&simplevent.Message[*event.MessageEventContent]{
				Type:          bridgev2.RemoteEventEdit,
				PortalKey:     networkid.PortalKey{ID: p.room, Receiver: c.login.ID},
				Sender:        bridgev2.EventSender{ForceEditOrigSender: true},
				Timestamp:     time.Now(),
				TargetMessage: p.guid,
				Data:          content,
				ConvertEditFunc: func(_ context.Context, _ *bridgev2.Portal, _ bridgev2.MatrixAPI, existing []*database.Message, content *event.MessageEventContent) (*bridgev2.ConvertedEdit, error) {
					return &bridgev2.ConvertedEdit{ModifiedParts: []*bridgev2.ConvertedEditPart{{Part: existing[0], Type: event.EventMessage, Content: content}}}, nil
				},
			})
			queued++
		}
	}
	zerolog.Ctx(ctx).Info().Str("event", "describe_placeholders").Int("backfilled", len(backfilled)).Int("queued", queued).
		Msg("Describing group changes bridged as unsupported")
	return nil
}

// placeholderEdit is a group change as the bridge sends it now, kept with the sender of the
// placeholder it replaces: the bot sent your own changes before it could tell they were yours.
func placeholderEdit(m core.Message, sentByBot bool, selfName string) *event.MessageEventContent {
	switch {
	case !m.IsFromMe && m.Sender == "":
		return &event.MessageEventContent{MsgType: event.MsgNotice, Body: m.Text}
	case sentByBot:
		return &event.MessageEventContent{MsgType: event.MsgNotice, Body: selfName + " " + m.Text}
	}
	return &event.MessageEventContent{MsgType: event.MsgEmote, Body: m.Text}
}
