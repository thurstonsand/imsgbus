package matrix

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/thurstonsand/imsgbus/internal/core"
)

// textPart is the part ID of a message's text; its attachments are "file-0", "file-1", and so on.
const textPart networkid.PartID = ""

// convertMessage turns an iMessage into Matrix parts: each attachment, uploaded, then the text.
func convertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, msg core.Message) (*bridgev2.ConvertedMessage, error) {
	converted := &bridgev2.ConvertedMessage{ReplyTo: replyTo(msg.ReplyTo)}
	for i, file := range msg.Attachments {
		content, err := convertFile(ctx, portal, intent, msg.GUID, file)
		if err != nil {
			return nil, err
		}
		evtType := event.EventMessage
		if file.Sticker && content.MsgType == event.MsgImage {
			evtType, content.MsgType = event.EventSticker, ""
		}
		converted.Parts = append(converted.Parts, &bridgev2.ConvertedMessagePart{
			ID:      networkid.PartID(fmt.Sprintf("file-%d", i)),
			Type:    evtType,
			Content: content,
		})
	}
	if msg.Text != "" || len(converted.Parts) == 0 {
		content := &event.MessageEventContent{MsgType: event.MsgText, Body: msg.Text}
		switch {
		// An emote reads after its sender's name, as Messages shows a group change.
		case msg.Announcement && (msg.IsFromMe || msg.Sender != ""):
			content.MsgType = event.MsgEmote
		case msg.Announcement:
			content.MsgType = event.MsgNotice
		case msg.Text == "":
			zerolog.Ctx(ctx).Warn().Str("event", "unsupported").Str("chat_guid", msg.ChatGUID).Str("guid", msg.GUID).Str("app", msg.App).
				Msg("Bridging a message with neither text nor files as unsupported")
			content = &event.MessageEventContent{MsgType: event.MsgNotice, Body: "Unsupported message; open it on an Apple device"}
		}
		converted.Parts = append(converted.Parts, &bridgev2.ConvertedMessagePart{ID: textPart, Type: event.EventMessage, Content: content})
	}
	return converted, nil
}

func convertFile(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, guid string, file core.Attachment) (*event.MessageEventContent, error) {
	if file.Path == "" {
		zerolog.Ctx(ctx).Info().Str("event", "attachment_missing").Str("chat_guid", string(portal.ID)).Str("guid", guid).Str("mime", file.MIME).Msg("Attachment isn't on the Mac")
		return &event.MessageEventContent{MsgType: event.MsgNotice, Body: file.Name + " isn't downloaded on the Mac yet; open it on an Apple device"}, nil
	}
	data, err := os.ReadFile(file.Path)
	if err != nil {
		return nil, fmt.Errorf("read attachment %s: %w", file.Name, err)
	}
	url, encrypted, err := intent.UploadMedia(ctx, portal.MXID, data, file.Name, file.MIME)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaReuploadFailed, err)
	}
	content := &event.MessageEventContent{
		MsgType:  msgType(file.MIME),
		Body:     file.Name,
		FileName: file.Name,
		URL:      url,
		File:     encrypted,
		Info:     &event.FileInfo{MimeType: file.MIME, Size: len(data)},
	}
	return content, nil
}

func msgType(mime string) event.MessageType {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return event.MsgImage
	case strings.HasPrefix(mime, "video/"):
		return event.MsgVideo
	case strings.HasPrefix(mime, "audio/"):
		return event.MsgAudio
	}
	return event.MsgFile
}

// convertEdit replaces the text part of an edited message.
func convertEdit(_ context.Context, _ *bridgev2.Portal, _ bridgev2.MatrixAPI, existing []*database.Message, edit core.Edit) (*bridgev2.ConvertedEdit, error) {
	for _, part := range existing {
		if part.PartID == textPart {
			return &bridgev2.ConvertedEdit{ModifiedParts: []*bridgev2.ConvertedEditPart{{
				Part:    part,
				Type:    event.EventMessage,
				Content: &event.MessageEventContent{MsgType: event.MsgText, Body: edit.Text},
			}}}, nil
		}
	}
	return nil, fmt.Errorf("message %s has no text to edit", edit.GUID)
}

func replyTo(guid string) *networkid.MessageOptionalPartID {
	if guid == "" {
		return nil
	}
	return &networkid.MessageOptionalPartID{MessageID: networkid.MessageID(guid)}
}
