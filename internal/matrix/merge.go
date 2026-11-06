package matrix

import (
	"context"

	"maunium.net/go/mautrix/bridgev2/database"

	"github.com/thurstonsand/imsgbus/internal/core"
)

// mergePortals leaves each conversation one portal, keyed by its ID, where imsgbus used to give
// each of a person's chats one, keyed by the chat's GUID. A conversation with one portal keeps its
// room under the new key. One with several loses its rooms, and syncChats creates its room anew
// with the history of all its chats, since a room can't take messages from before its newest one.
// It does nothing once every portal is keyed by its conversation.
func (c *Client) mergePortals(ctx context.Context) error {
	portals, err := c.login.Bridge.DB.Portal.GetAll(ctx)
	if err != nil {
		return err
	}
	byConversation := map[string][]*database.Portal{}
	for _, p := range portals {
		if p.Receiver != c.login.ID {
			continue
		}
		id := core.ConversationID(string(p.ID))
		byConversation[id] = append(byConversation[id], p)
	}
	for id, merging := range byConversation {
		log := c.login.Log.With().Str("event", "merge_portals").Str("conversation", id).Int("portals", len(merging)).Logger()
		if len(merging) == 1 {
			if string(merging[0].ID) == id {
				continue
			}
			result, _, err := c.login.Bridge.ReIDPortal(ctx, merging[0].PortalKey, c.portalKey(id))
			if err != nil {
				return err
			}
			log.Info().Str("chat_guid", string(merging[0].ID)).Stringer("result", result).Msg("Moved a chat's portal to its conversation")
			continue
		}
		for _, p := range merging {
			portal, err := c.login.Bridge.GetExistingPortalByKey(ctx, p.PortalKey)
			if err != nil {
				return err
			}
			if err = portal.Delete(ctx); err != nil {
				return err
			}
			if portal.MXID != "" {
				if err = c.login.Bridge.Bot.DeleteRoom(ctx, portal.MXID, false); err != nil {
					return err
				}
			}
			log.Info().Str("chat_guid", string(p.ID)).Stringer("room", portal.MXID).Msg("Deleted a chat's room to merge it into its conversation's")
		}
	}
	return nil
}
