package matrix

import (
	"context"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

// mergePortals leaves each conversation one portal, keyed by its ID, as conversations change: an
// address added to a contact joins their conversation, and one removed leaves it. A one-on-one
// room follows its other person's address, so a contact whose ID changed keeps their room. A
// conversation with several portals keeps the room with the most messages, under its new key,
// and loses the others; the room it keeps backfills the history of every chat it now holds from
// before its oldest message and after its newest, but not between them, where a room takes no
// history. A room whose conversation is gone is deleted. It reports whether it changed anything,
// and changes nothing once every portal is keyed by its conversation.
func (c *Client) mergePortals(ctx context.Context) (bool, error) {
	c.merging.Lock()
	defer c.merging.Unlock()
	portals, err := c.login.Bridge.DB.Portal.GetAll(ctx)
	if err != nil {
		return false, err
	}
	byConversation := map[string][]*database.Portal{}
	for _, p := range portals {
		if p.Receiver != c.login.ID {
			continue
		}
		id := c.core.ConversationID(string(p.ID))
		if p.OtherUserID != "" {
			id = c.core.ConversationID(oneOnOne + string(p.OtherUserID))
		}
		byConversation[id] = append(byConversation[id], p)
	}
	changed, backfill := false, false
	for id, merging := range byConversation {
		log := c.login.Log.With().Str("event", "merge_portals").Str("conversation", id).Int("portals", len(merging)).Logger()
		if len(merging) == 1 && string(merging[0].ID) == id {
			continue
		}
		changed = true
		var keep *database.Portal
		if id != "" {
			if keep, err = c.largestRoom(ctx, merging); err != nil {
				return changed, err
			}
		}
		for _, p := range merging {
			if p == keep {
				continue
			}
			if err := c.deletePortal(ctx, p.PortalKey); err != nil {
				return changed, err
			}
			log.Info().Str("portal", string(p.ID)).Msg("Deleted a room merged into another, or whose conversation is gone")
		}
		if keep == nil {
			continue
		}
		if string(keep.ID) != id {
			result, _, err := c.login.Bridge.ReIDPortal(ctx, keep.PortalKey, c.portalKey(id))
			if err != nil {
				return changed, err
			}
			log.Info().Str("portal", string(keep.ID)).Stringer("result", result).Msg("Moved a room to its conversation")
		}
		if len(merging) > 1 {
			key := c.portalKey(id)
			if err := c.login.Bridge.DB.BackfillTask.EnsureExists(ctx, key, c.login.ID); err != nil {
				return changed, err
			}
			if err := c.login.Bridge.DB.BackfillTask.MarkNotDone(ctx, key, c.login.ID); err != nil {
				return changed, err
			}
			c.lock.Lock()
			c.refill[id] = true
			c.lock.Unlock()
			backfill = true
		}
	}
	if backfill {
		c.login.Bridge.WakeupBackfillQueue()
	}
	return changed, nil
}

// oneOnOne starts the GUID of a one-on-one chat; the other person's address follows it.
const oneOnOne = "any;-;"

// largestRoom is the portal with a room that holds the most messages, or the first portal when
// none has a room.
func (c *Client) largestRoom(ctx context.Context, portals []*database.Portal) (*database.Portal, error) {
	largest, most := portals[0], -1
	for _, p := range portals {
		if p.MXID == "" {
			continue
		}
		var count int
		err := c.login.Bridge.DB.QueryRow(ctx, `SELECT count(*) FROM message WHERE bridge_id = $1 AND room_id = $2 AND room_receiver = $3`,
			c.login.Bridge.ID, p.ID, p.Receiver).Scan(&count)
		if err != nil {
			return nil, err
		}
		if count > most {
			largest, most = p, count
		}
	}
	return largest, nil
}

// deletePortal deletes a portal and its room, if something else hasn't already.
func (c *Client) deletePortal(ctx context.Context, key networkid.PortalKey) error {
	portal, err := c.login.Bridge.GetExistingPortalByKey(ctx, key)
	if err != nil || portal == nil {
		return err
	}
	if err = portal.Delete(ctx); err != nil {
		return err
	}
	if portal.MXID != "" {
		return c.login.Bridge.Bot.DeleteRoom(ctx, portal.MXID, false)
	}
	return nil
}

// regroup brings rooms in line with conversations after Contacts changes, while connected.
func (c *Client) regroup() {
	c.lock.Lock()
	ctx := c.ctx
	c.lock.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}
	changed, err := c.mergePortals(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to regroup rooms after Contacts changed")
	}
	if changed {
		c.syncChats(ctx)
	}
}
