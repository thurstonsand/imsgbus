package core

import (
	"strings"
	"time"

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
}

// Attachment is a file on a message. Path is empty when the file isn't on this Mac, such as one
// still in iCloud.
type Attachment struct {
	Name string
	MIME string
	Path string
}

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
		file := Attachment{Name: a.TransferName, MIME: a.MIMEType, Path: a.OriginalPath}
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
	}
}

func toEvent(m imsg.Message) Event {
	text := toMessage(m).Text
	switch {
	// macOS 26 records an unsend in message_summary_info without date_retracted, which imsg
	// then reports as an edit to nothing; Apple never lets an edit empty a message.
	case m.Type == "unsent" || (m.Type == "edited" && text == "" && len(m.Attachments) == 0):
		return Unsend{GUID: m.GUID, ChatGUID: m.ChatGUID, Sender: m.Sender, IsFromMe: m.IsFromMe, Time: m.CreatedAt}
	case m.Type == "edited":
		return Edit{GUID: m.GUID, ChatGUID: m.ChatGUID, Sender: m.Sender, IsFromMe: m.IsFromMe, Text: text, Time: m.CreatedAt}
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
