package imsg

import (
	"context"
	"time"
)

// Chat is imsg's chat object, from chats.list.
type Chat struct {
	ID           int64    `json:"id"`
	GUID         string   `json:"guid"`
	DisplayName  string   `json:"display_name"`
	ContactName  string   `json:"contact_name"`
	IsGroup      bool     `json:"is_group"`
	Participants []string `json:"participants"`
	AccountID    string   `json:"account_id"`
	LastHandle   string   `json:"last_addressed_handle"`
}

// Message is imsg's message object, from history and watch.
type Message struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	ChatID      int64     `json:"chat_id"`
	ChatGUID    string    `json:"chat_guid"`
	IsGroup     bool      `json:"is_group"`
	Sender      string    `json:"sender"`
	SenderName  string    `json:"sender_name"`
	IsFromMe    bool      `json:"is_from_me"`
	Text        string    `json:"text"`
	CreatedAt   time.Time `json:"created_at"`
	ReplyToGUID string    `json:"reply_to_guid"`
	// Type is "edited" or "unsent" for a mutation of an earlier row, empty for a new row.
	Type string `json:"type"`
}

// Watch notification methods. "watch.overflow" and "error" end their subscription.
const (
	NotifyMessage  = "message"
	NotifyOverflow = "watch.overflow"
	NotifyError    = "error"
)

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

// ErrorNotification is the params of the terminal "error" notification for a failed watch.
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

// History returns a chat's newest messages, newest first, by the chat's row ID.
func (c *Client) History(ctx context.Context, chatID int64, limit int) ([]Message, error) {
	var result struct {
		Messages []Message `json:"messages"`
	}
	err := c.Call(ctx, "messages.history", map[string]any{"chat_id": chatID, "limit": limit}, &result)
	return result.Messages, err
}

// WatchSubscribe streams new rows after sinceRowID (0 starts at the current tail) as
// notifications to the client's handler, and returns the subscription ID.
func (c *Client) WatchSubscribe(ctx context.Context, sinceRowID int64) (int64, error) {
	var result struct {
		Subscription int64 `json:"subscription"`
	}
	err := c.Call(ctx, "watch.subscribe", map[string]any{"since_rowid": sinceRowID}, &result)
	return result.Subscription, err
}

// WatchUnsubscribe ends a watch.
func (c *Client) WatchUnsubscribe(ctx context.Context, subscription int64) error {
	return c.Call(ctx, "watch.unsubscribe", map[string]any{"subscription": subscription}, nil)
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
