package imsg

import (
	"context"
	"time"
)

// Chat is imsg's chat object, from chats.list.
type Chat struct {
	ID           int64    `json:"id"`
	GUID         string   `json:"guid"`
	Identifier   string   `json:"identifier"`
	Name         string   `json:"name"`
	DisplayName  string   `json:"display_name"`
	ContactName  string   `json:"contact_name"`
	IsGroup      bool     `json:"is_group"`
	Participants []string `json:"participants"`
	AccountID    string   `json:"account_id"`
	LastHandle   string   `json:"last_addressed_handle"`
	// LastMessage is set by chats.list only.
	LastMessage time.Time `json:"last_message_at"`
}

// Message is imsg's message object, from history and watch.
type Message struct {
	ID           int64        `json:"id"`
	GUID         string       `json:"guid"`
	ChatID       int64        `json:"chat_id"`
	ChatGUID     string       `json:"chat_guid"`
	ChatName     string       `json:"chat_name"`
	ChatIdent    string       `json:"chat_identifier"`
	Participants []string     `json:"participants"`
	IsGroup      bool         `json:"is_group"`
	Sender       string       `json:"sender"`
	SenderName   string       `json:"sender_name"`
	IsFromMe     bool         `json:"is_from_me"`
	Text         string       `json:"text"`
	CreatedAt    time.Time    `json:"created_at"`
	ReplyToGUID  string       `json:"reply_to_guid"`
	Attachments  []Attachment `json:"attachments"`
	// Type is "edited" or "unsent" for a mutation of an earlier row, empty for a new row.
	Type string `json:"type"`

	IsReaction    bool   `json:"is_reaction"`
	ReactionType  string `json:"reaction_type"`
	ReactionEmoji string `json:"reaction_emoji"`
	IsReactionAdd bool   `json:"is_reaction_add"`
	ReactedTo     string `json:"reacted_to_guid"`
}

// Attachment is a file on a message.
type Attachment struct {
	TransferName  string `json:"transfer_name"`
	MIMEType      string `json:"mime_type"`
	TotalBytes    int64  `json:"total_bytes"`
	Missing       bool   `json:"missing"`
	OriginalPath  string `json:"original_path"`
	ConvertedPath string `json:"converted_path"`
	ConvertedMIME string `json:"converted_mime_type"`
}

// Subscription notification methods. All but "message" and "bridge.event" end their
// subscription.
const (
	NotifyMessage        = "message"
	NotifyOverflow       = "watch.overflow"
	NotifyError          = "error"
	NotifyBridgeEvent    = "bridge.event"
	NotifyBridgeOverflow = "bridge.events.overflow"
	NotifyBridgeError    = "bridge.events.error"
)

// BridgeEventNotification is the params of a "bridge.event" notification.
type BridgeEventNotification struct {
	Subscription int64 `json:"subscription"`
	Event        struct {
		Event string `json:"event"`
		Data  struct {
			ChatGUID string `json:"chatGuid"`
		} `json:"data"`
	} `json:"event"`
}

// MessageNotification is the params of a watch "message" notification.
type MessageNotification struct {
	Subscription int64   `json:"subscription"`
	Message      Message `json:"message"`
}

// OverflowNotification is the params of the terminal "watch.overflow" notification.
type OverflowNotification struct {
	Subscription     int64 `json:"subscription"`
	ResumeAfterRowID int64 `json:"resume_after_rowid"`
}

// ErrorNotification is the params of a terminal "error" or "bridge.events.error" notification.
type ErrorNotification struct {
	Subscription int64 `json:"subscription"`
	Error        struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ChatsList returns the most recently active chats.
func (c *Client) ChatsList(ctx context.Context, limit int) ([]Chat, error) {
	var result struct {
		Chats []Chat `json:"chats"`
	}
	err := c.Call(ctx, "chats.list", map[string]any{"limit": limit}, &result)
	return result.Chats, err
}

// History returns a chat's newest messages, newest first, by the chat's row ID. A non-zero start
// or end bounds them by time; withFiles adds attachments, converted to formats anything can show.
func (c *Client) History(ctx context.Context, chatID int64, limit int, start, end time.Time, withFiles bool) ([]Message, error) {
	params := map[string]any{"chat_id": chatID, "limit": limit}
	if !start.IsZero() {
		params["start"] = start.Format(time.RFC3339Nano)
	}
	if !end.IsZero() {
		params["end"] = end.Format(time.RFC3339Nano)
	}
	if withFiles {
		params["attachments"] = true
		params["convert_attachments"] = true
	}
	var result struct {
		Messages []Message `json:"messages"`
	}
	err := c.Call(ctx, "messages.history", params, &result)
	return result.Messages, err
}

// ChatsGet returns one chat by row ID.
func (c *Client) ChatsGet(ctx context.Context, chatID int64) (Chat, error) {
	var chat Chat
	err := c.Call(ctx, "chats.get", map[string]any{"chat_id": chatID}, &chat)
	return chat, err
}

// WatchSubscribe streams new rows after sinceRowID (0 starts at the current tail), with their
// attachments and tapbacks, and edits and unsends of earlier rows, as notifications to the
// client's handler, and returns the subscription ID.
func (c *Client) WatchSubscribe(ctx context.Context, sinceRowID int64) (int64, error) {
	var result struct {
		Subscription int64 `json:"subscription"`
	}
	err := c.Call(ctx, "watch.subscribe", map[string]any{
		"since_rowid":         sinceRowID,
		"attachments":         true,
		"convert_attachments": true,
		"include_reactions":   true,
		"buffer_limit":        4096,
	}, &result)
	return result.Subscription, err
}

// WatchUnsubscribe ends a watch.
func (c *Client) WatchUnsubscribe(ctx context.Context, subscription int64) error {
	return c.Call(ctx, "watch.unsubscribe", map[string]any{"subscription": subscription}, nil)
}

// BridgeEventsSubscribe streams typing events from the IMCore bridge, from now on.
func (c *Client) BridgeEventsSubscribe(ctx context.Context) (int64, error) {
	var result struct {
		Subscription int64 `json:"subscription"`
	}
	err := c.Call(ctx, "bridge.events.subscribe", map[string]any{}, &result)
	return result.Subscription, err
}

// Typing shows or clears this account's typing indicator in a chat.
func (c *Client) Typing(ctx context.Context, chatGUID string, typing bool) error {
	return c.Call(ctx, "typing", map[string]any{"chat_guid": chatGUID, "typing": typing}, nil)
}

// SendTracked sends text to a chat with a caller-chosen UUID, which imsg lowercases and uses as
// the message GUID.
func (c *Client) SendTracked(ctx context.Context, chatGUID, attemptID, text, replyTo string) error {
	params := map[string]any{"chat_guid": chatGUID, "text": text, "attempt_id": attemptID}
	if replyTo != "" {
		params["reply_to"] = replyTo
	}
	return c.Call(ctx, "send.tracked", params, nil)
}

// Read marks every message in a chat read, which sends a read receipt when the chat has them on.
func (c *Client) Read(ctx context.Context, chatGUID string) error {
	return c.Call(ctx, "read", map[string]any{"chat_guid": chatGUID}, nil)
}

// SendAttachment sends a file to a chat, as a voice message when audio is set, and returns the
// new message's GUID.
func (c *Client) SendAttachment(ctx context.Context, chatGUID, path, replyTo string, audio bool) (string, error) {
	params := map[string]any{"chat_guid": chatGUID, "file": path, "audio": audio}
	if replyTo != "" {
		params["reply_to"] = replyTo
	}
	var result struct {
		GUID string `json:"guid"`
	}
	err := c.Call(ctx, "send.attachment", params, &result)
	return result.GUID, err
}

// Tapback adds or removes a tapback: love, like, dislike, laugh, emphasis, question, or an emoji.
func (c *Client) Tapback(ctx context.Context, chatGUID, messageGUID, reaction string, remove bool) error {
	return c.Call(ctx, "tapback", map[string]any{
		"chat_guid": chatGUID, "message_guid": messageGUID, "reaction": reaction, "remove": remove,
	}, nil)
}

// Edit replaces a sent message's text.
func (c *Client) Edit(ctx context.Context, chatGUID, messageGUID, text string) error {
	return c.Call(ctx, "message.edit", map[string]any{"chat_guid": chatGUID, "message_guid": messageGUID, "text": text}, nil)
}

// Unsend retracts a sent message for everyone.
func (c *Client) Unsend(ctx context.Context, chatGUID, messageGUID string) error {
	return c.Call(ctx, "message.unsend", map[string]any{"chat_guid": chatGUID, "message_guid": messageGUID}, nil)
}
