package seabird_irc

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"gopkg.in/irc.v4"

	"github.com/seabird-chat/seabird-go"
	"github.com/seabird-chat/seabird-go/pb"
)

func TestEventFromPrivmsg(t *testing.T) {
	const nick = "bot"

	text := seabird.NewTextBlock
	user := &pb.User{Id: "alice", DisplayName: "alice"}
	source := &pb.ChannelSource{ChannelId: "#chan", User: user}

	tests := []struct {
		name string
		line string
		want *pb.ChatEvent
	}{
		{
			"message",
			":alice!u@h PRIVMSG #chan :hello there",
			&pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
				Source: source, RootBlock: text("hello there"),
			}}},
		},
		{
			"formatted message",
			":alice!u@h PRIVMSG #chan :\x02hello\x02 there",
			&pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
				Source:    source,
				RootBlock: seabird.NewContainerBlock(seabird.NewBoldBlock(text("hello")), text(" there")),
			}}},
		},
		{
			"command",
			":alice!u@h PRIVMSG #chan :!cmd some arg",
			&pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{
				Source: source, Command: "cmd", Arg: "some arg",
			}}},
		},
		{
			"command without arg",
			":alice!u@h PRIVMSG #chan :!cmd",
			&pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{
				Source: source, Command: "cmd",
			}}},
		},
		{
			"formatted command",
			":alice!u@h PRIVMSG #chan :\x02!cmd\x02 \x0304arg",
			&pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{
				Source: source, Command: "cmd", Arg: "arg",
			}}},
		},
		{
			"mention",
			":alice!u@h PRIVMSG #chan :bot: \x02hi\x02 there ",
			&pb.ChatEvent{Inner: &pb.ChatEvent_Mention{Mention: &pb.MentionEvent{
				Source:    source,
				RootBlock: seabird.NewContainerBlock(seabird.NewBoldBlock(text("hi")), text(" there")),
			}}},
		},
		{
			"formatted mention",
			":alice!u@h PRIVMSG #chan :\x02bot\x02: hi",
			&pb.ChatEvent{Inner: &pb.ChatEvent_Mention{Mention: &pb.MentionEvent{
				Source: source, RootBlock: text("hi"),
			}}},
		},
		{
			"nick alone is not a mention",
			":alice!u@h PRIVMSG #chan :bot:",
			&pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
				Source: source, RootBlock: text("bot:"),
			}}},
		},
		{
			"nick without space is not a mention",
			":alice!u@h PRIVMSG #chan :bot:x",
			&pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
				Source: source, RootBlock: text("bot:x"),
			}}},
		},
		{
			"action",
			":alice!u@h PRIVMSG #chan :\x01ACTION waves \x1Dwildly\x1D\x01",
			&pb.ChatEvent{Inner: &pb.ChatEvent_Action{Action: &pb.ActionEvent{
				Source:    source,
				RootBlock: seabird.NewContainerBlock(text("waves "), seabird.NewItalicsBlock(text("wildly"))),
			}}},
		},
		{
			"private message",
			":alice!u@h PRIVMSG bot :\x0301,01secret",
			&pb.ChatEvent{Inner: &pb.ChatEvent_PrivateMessage{PrivateMessage: &pb.PrivateMessageEvent{
				Source: user, RootBlock: seabird.NewSpoilerBlock(text("secret")),
			}}},
		},
		{
			"private action",
			":alice!u@h PRIVMSG bot :\x01ACTION waves\x01",
			&pb.ChatEvent{Inner: &pb.ChatEvent_PrivateAction{PrivateAction: &pb.PrivateActionEvent{
				Source: user, RootBlock: text("waves"),
			}}},
		},
		{"other ctcp", ":alice!u@h PRIVMSG #chan :\x01VERSION\x01", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := irc.ParseMessage(tt.line)
			if err != nil {
				t.Fatal(err)
			}

			got := eventFromPrivmsg(msg, nick, "!")
			if !proto.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
