package seabird_irc

import (
	"strings"

	"gopkg.in/irc.v4"

	"github.com/seabird-chat/seabird-go/pb"
)

// eventFromPrivmsg converts an IRC PRIVMSG into the chat event to send to
// seabird-core. It returns nil for messages that should be ignored.
func eventFromPrivmsg(msg *irc.Message, currentNick, cmdPrefix string) *pb.ChatEvent {
	sender := msg.Prefix.Name
	user := &pb.User{Id: sender, DisplayName: sender}

	body := msg.Trailing()
	isAction := false

	if ctcp, ok := parseCtcp(msg); ok {
		// Ignore everything but ACTION CTCP events
		if ctcp.Action != "ACTION" {
			return nil
		}

		body = ctcp.Text
		isAction = true
	}

	// Events carry only the block tree: core derives their text from it.
	segments := parseSegments(body)
	block := segmentsBlock(segments)

	// Commands and mentions are detected without formatting.
	text := plainText(segments)

	if msg.Params[0] == currentNick {
		if isAction {
			return &pb.ChatEvent{Inner: &pb.ChatEvent_PrivateAction{PrivateAction: &pb.PrivateActionEvent{
				Source:    user,
				RootBlock: block,
			}}}
		}

		return &pb.ChatEvent{Inner: &pb.ChatEvent_PrivateMessage{PrivateMessage: &pb.PrivateMessageEvent{
			Source:    user,
			RootBlock: block,
		}}}
	}

	source := &pb.ChannelSource{ChannelId: msg.Params[0], User: user}

	switch {
	case isAction:
		return &pb.ChatEvent{Inner: &pb.ChatEvent_Action{Action: &pb.ActionEvent{
			Source:    source,
			RootBlock: block,
		}}}

	case strings.HasPrefix(text, cmdPrefix):
		parts := strings.SplitN(text, " ", 2)
		if len(parts) < 2 {
			parts = append(parts, "")
		}

		return &pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{
			Source:  source,
			Command: strings.TrimPrefix(parts[0], cmdPrefix),
			Arg:     parts[1],
		}}}
	}

	if n, ok := mentionPrefixLen(text, currentNick); ok {
		rest := trimSegments(segments, n)

		return &pb.ChatEvent{Inner: &pb.ChatEvent_Mention{Mention: &pb.MentionEvent{
			Source:    source,
			RootBlock: segmentsBlock(rest),
		}}}
	}

	return &pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
		Source:    source,
		RootBlock: block,
	}}}
}
