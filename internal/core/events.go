package core

import (
	"strings"
	"time"

	"github.com/thurstonsand/imsgbus/internal/chatdb"
	"github.com/thurstonsand/imsgbus/internal/imsg"
)

// Event is something that happened in a chat: a Message, Tapback, Edit, Unsend, or ChatChange.
type Event interface{ event() }

// Message is one new message in any chat, from any of the account's devices.
type Message struct {
	// RowID orders messages within this Mac's database; watches resume after it.
	RowID       int64
	GUID        string
	ChatGUID    string
	IsGroup     bool
	Sender      string
	IsFromMe    bool
	Text        string
	Time        time.Time
	ReplyTo     string
	Attachments []Attachment
	// App is the iMessage app that made the message, such as
	// "com.apple.messages.URLBalloonProvider" for a link, or empty for a plain message.
	App string
	// Announcement is a change to a group, such as someone being added, that Text describes. It
	// reads after the sender's name ("added Dana"), or stands alone when there is no sender.
	Announcement bool
}

// Attachment is a file on a message. Path is empty when the file isn't on this Mac, such as one
// still in iCloud.
type Attachment struct {
	Name string
	MIME string
	Path string
	// Sticker is an image placed as a sticker, such as a Genmoji or Memoji sent on its own.
	Sticker bool
}

// linkPreview is the attachment suffix of the preview Messages renders for a link; the link
// itself is in the text.
const linkPreview = ".pluginPayloadAttachment"

// Tapback is someone adding or removing their tapback on a message. One person has at most one
// tapback on a message, so a new one replaces theirs.
type Tapback struct {
	RowID    int64
	GUID     string
	ChatGUID string
	Sender   string
	IsFromMe bool
	Target   string
	// Emoji is the tapback as an emoji, also for the six classic ones.
	Emoji   string
	Removed bool
	Time    time.Time
}

// Edit is a sent message's new text.
type Edit struct {
	RowID    int64
	GUID     string
	ChatGUID string
	Sender   string
	IsFromMe bool
	Text     string
	Time     time.Time
}

// Unsend is a message its sender retracted.
type Unsend struct {
	GUID     string
	ChatGUID string
	Sender   string
	IsFromMe bool
	Time     time.Time
}

// ChatChange is a chat's name or members changing; it comes before the message that shows it.
type ChatChange struct{ Chat Chat }

func (Message) event()    {}
func (Tapback) event()    {}
func (Edit) event()       {}
func (Unsend) event()     {}
func (ChatChange) event() {}

// Address splits a handle into the phone number or email it is and the category Messages' text
// message filter sorted its sender into, which chat.db appends in parentheses: "smsfp" for
// promotions, "smsft" for transactions (with "_fi" finance, "_or" orders and "_rm" reminders),
// "filtered" for unknown senders. category is empty for anyone the filter left alone.
func Address(handle string) (address, category string) {
	address, rest, filtered := strings.Cut(handle, "(")
	if !filtered {
		return handle, ""
	}
	category, _, _ = strings.Cut(rest, ")")
	return address, category
}

// addressConversation is the conversation a chat is part of among a single address's chats.
// Messages' filter files a message from someone not in Contacts under a chat of its own for each
// category, whose GUID carries the category after the address ("any;-;+15551234567(smsft)"), so
// one address can have several chats; their conversation is the GUID without it.
func addressConversation(chatGUID string) string {
	prefix, handle, ok := strings.Cut(chatGUID, ";-;")
	if !ok {
		return chatGUID
	}
	address, _ := Address(handle)
	return prefix + ";-;" + address
}

// Filtered is whether a chat is one Messages' filter keeps out of the conversation list, for
// transactions, promotions, or unknown senders.
func Filtered(chatGUID string) bool {
	_, handle, _ := strings.Cut(chatGUID, ";-;")
	_, category := Address(handle)
	return category != ""
}

// classicTapbacks maps imsg's tapback types to the emoji they show as, and back.
var classicTapbacks = map[string]string{
	"love":     "❤️",
	"like":     "👍",
	"dislike":  "👎",
	"laugh":    "😂",
	"emphasis": "‼️",
	"question": "❓",
}

// tapbackReaction is what imsg's tapback method takes for an emoji: a classic type, or the emoji.
func tapbackReaction(emoji string) string {
	for kind, e := range classicTapbacks {
		if e == emoji || strings.TrimSuffix(e, "\uFE0F") == emoji {
			return kind
		}
	}
	return emoji
}

func toMessage(m imsg.Message) Message {
	files := make([]Attachment, 0, len(m.Attachments))
	for _, a := range m.Attachments {
		if strings.HasSuffix(a.TransferName, linkPreview) {
			continue
		}
		file := Attachment{Name: a.TransferName, MIME: a.MIMEType, Path: a.OriginalPath, Sticker: a.IsSticker}
		if a.ConvertedPath != "" {
			file.Path, file.MIME = a.ConvertedPath, a.ConvertedMIME
		}
		if a.Missing {
			file.Path = ""
		}
		files = append(files, file)
	}
	return Message{
		RowID:    m.ID,
		GUID:     m.GUID,
		ChatGUID: m.ChatGUID,
		IsGroup:  m.IsGroup,
		Sender:   m.Sender,
		IsFromMe: m.IsFromMe,
		// imsg keeps U+FFFC where an attachment sits in the text; attachments aren't text.
		Text:        strings.TrimSpace(strings.ReplaceAll(m.Text, "\uFFFC", "")),
		Time:        m.CreatedAt,
		ReplyTo:     m.ReplyToGUID,
		Attachments: files,
		App:         m.BalloonBundleID,
	}
}

// announcementText describes a, after the name of whoever made it, or on its own when no one did.
func announcementText(a chatdb.Announcement, name func(string) string) string {
	subject := "someone"
	if address, _ := Address(a.Subject); address != "" {
		subject = name(address)
	}
	switch {
	case a.Kind == chatdb.Added && a.Unattributed:
		return subject + " was added to the conversation"
	case a.Kind == chatdb.Added:
		return "added " + subject + " to the conversation"
	case a.Kind == chatdb.Removed && a.Unattributed:
		return subject + " was removed from the conversation"
	case a.Kind == chatdb.Removed:
		return "removed " + subject + " from the conversation"
	case a.Kind == chatdb.Renamed && a.Unattributed:
		return "The conversation was named “" + a.Title + "”"
	case a.Kind == chatdb.Renamed:
		return "named the conversation “" + a.Title + "”"
	case a.Kind == chatdb.NameRemoved && a.Unattributed:
		return "The conversation's name was removed"
	case a.Kind == chatdb.NameRemoved:
		return "removed the conversation's name"
	case a.Kind == chatdb.Left:
		return "left the conversation"
	case a.Kind == chatdb.PhotoChanged && a.Unattributed:
		return "The group photo was changed"
	case a.Kind == chatdb.PhotoChanged:
		return "changed the group photo"
	case a.Kind == chatdb.PhotoRemoved && a.Unattributed:
		return "The group photo was removed"
	}
	return "removed the group photo"
}

func toEvent(m imsg.Message) Event {
	text := toMessage(m).Text
	switch {
	// macOS 26 records an unsend in message_summary_info without date_retracted, which imsg
	// then reports as an edit to nothing; Apple never lets an edit empty a message.
	case m.Type == "unsent" || (m.Type == "edited" && text == "" && len(m.Attachments) == 0):
		return Unsend{GUID: m.GUID, ChatGUID: m.ChatGUID, Sender: m.Sender, IsFromMe: m.IsFromMe, Time: m.CreatedAt}
	case m.Type == "edited":
		return Edit{RowID: m.ID, GUID: m.GUID, ChatGUID: m.ChatGUID, Sender: m.Sender, IsFromMe: m.IsFromMe, Text: text, Time: m.CreatedAt}
	case m.IsReaction:
		emoji := m.ReactionEmoji
		if classic, ok := classicTapbacks[m.ReactionType]; ok {
			emoji = classic
		}
		return Tapback{
			RowID: m.ID, GUID: m.GUID, ChatGUID: m.ChatGUID, Sender: m.Sender, IsFromMe: m.IsFromMe,
			Target: m.ReactedTo, Emoji: emoji, Removed: !m.IsReactionAdd, Time: m.CreatedAt,
		}
	}
	return toMessage(m)
}
