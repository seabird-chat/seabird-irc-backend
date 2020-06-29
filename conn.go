package seabird_irc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/go-irc/irc/v4"
	"github.com/go-irc/ircx"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/seabird-chat/seabird-irc-backend/pb"
)

type IRCConfig struct {
	IRCHost       string
	IRCID         string
	Nick          string
	User          string
	Name          string
	Pass          string
	CommandPrefix string
	Channels      []string

	Logger zerolog.Logger

	SeabirdHost  string
	SeabirdToken string
}

type Backend struct {
	id             string
	channels       []string
	cmdPrefix      string
	seabirdHost    string
	seabirdToken   string
	logger         zerolog.Logger
	ircSendLock    sync.Mutex
	irc            *ircx.Client
	grpc           pb.ChatIngestClient
	ingestSendLock sync.Mutex
	ingestStream   pb.ChatIngest_IngestEventsClient
	requestsLock   sync.Mutex
	requests       map[string]*pb.ChatRequest
}

func New(config IRCConfig) (*Backend, error) {
	var err error

	b := &Backend{
		id:           config.IRCID,
		channels:     config.Channels,
		logger:       config.Logger,
		cmdPrefix:    config.CommandPrefix,
		seabirdHost:  config.SeabirdHost,
		seabirdToken: config.SeabirdToken,
		requests:     make(map[string]*pb.ChatRequest),
	}

	b.irc, err = newIRCClient(&config, ircx.HandlerFunc(b.ircHandler))
	if err != nil {
		return nil, err
	}

	return b, nil
}

func (b *Backend) ircHandler(c *ircx.Client, msg *irc.Message) {
	switch msg.Command {
	case "001":
		for _, channel := range b.channels {
			_ = b.writeIRCMessage(&irc.Message{
				Command: "JOIN",
				Params:  []string{channel},
			}, nil)
		}
	case "PRIVMSG":
		b.handlePrivmsg(msg)
	case "JOIN":
		if msg.Prefix.Name == b.irc.CurrentNick() {
			b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_JoinChannel{JoinChannel: &pb.JoinChannelChatEvent{
				ChannelId:   msg.Params[0],
				DisplayName: msg.Params[0],
			}}})
		}
	case "PART":
		if msg.Prefix.Name == b.irc.CurrentNick() {
			b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_LeaveChannel{LeaveChannel: &pb.LeaveChannelChatEvent{
				ChannelId: msg.Params[0],
			}}})
		}
	case "KICK":
		if msg.Params[1] == b.irc.CurrentNick() {
			b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_LeaveChannel{LeaveChannel: &pb.LeaveChannelChatEvent{
				ChannelId: msg.Params[0],
			}}})
		}
	case "PONG":
		b.handlePong(msg.Trailing())
	case irc.RPL_TOPIC:
		b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_ChangeChannel{ChangeChannel: &pb.ChangeChannelChatEvent{
			ChannelId:   msg.Params[1],
			DisplayName: msg.Params[1],
			Topic:       msg.Trailing(),
		}}})
	default:
	}
}

func (b *Backend) popRequest(id string) *pb.ChatRequest {
	b.requestsLock.Lock()
	defer b.requestsLock.Unlock()

	ret := b.requests[id]
	delete(b.requests, id)
	return ret
}

func (b *Backend) writeSuccess(id string) {
	b.writeEvent(&pb.ChatEvent{
		Id:    id,
		Inner: &pb.ChatEvent_Success{Success: &pb.SuccessChatEvent{}},
	})
}

func (b *Backend) writeFailure(id string) {
	b.writeEvent(&pb.ChatEvent{
		Id:    id,
		Inner: &pb.ChatEvent_Failed{Failed: &pb.FailedChatEvent{}},
	})
}

func (b *Backend) handlePong(id string) {
	req := b.popRequest(id)
	if req == nil {
		return
	}

	switch v := req.Inner.(type) {
	case *pb.ChatRequest_SendMessage,
		*pb.ChatRequest_SendPrivateMessage,
		*pb.ChatRequest_PerformAction,
		*pb.ChatRequest_PerformPrivateAction:
		// TODO: we cheat and say all message sends succeeded. This is not
		// always true.
		b.writeSuccess(id)
	case *pb.ChatRequest_JoinChannel:
		if b.irc.Tracker.GetChannel(v.JoinChannel.ChannelName) != nil {
			b.writeSuccess(id)
		} else {
			b.writeFailure(id)
		}
	case *pb.ChatRequest_LeaveChannel:
		if b.irc.Tracker.GetChannel(v.LeaveChannel.ChannelId) == nil {
			b.writeSuccess(id)
		} else {
			b.writeFailure(id)
		}
	case *pb.ChatRequest_UpdateChannelInfo:
		info := b.irc.Tracker.GetChannel(v.UpdateChannelInfo.ChannelId)
		if info != nil && info.Topic == v.UpdateChannelInfo.Topic {
			b.writeSuccess(id)
		} else {
			b.writeFailure(id)
		}
	default:
		b.logger.Warn().Msgf("unknown request type in pong handler: %T", req)
	}
}

func (b *Backend) handlePrivmsg(msg *irc.Message) {
	lastArg := msg.Trailing()
	currentNick := b.irc.CurrentNick()

	if msg.Params[0] == currentNick {
		sender := msg.Prefix.Name
		message := lastArg

		if ctcp, ok := parseCtcp(msg); ok {
			// Ignore everything but ACTION CTCP events
			if ctcp.Action != "ACTION" {
				return
			}

			b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_PrivateAction{PrivateAction: &pb.PrivateActionEvent{
				Source: &pb.User{
					Id:          sender,
					DisplayName: sender,
				},
				Text: ctcp.Text,
			}}})
		} else {
			b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_PrivateMessage{PrivateMessage: &pb.PrivateMessageEvent{
				Source: &pb.User{
					Id:          sender,
					DisplayName: sender,
				},
				Text: message,
			}}})
		}
	} else {
		channel := msg.Params[0]
		sender := msg.Prefix.Name

		source := &pb.ChannelSource{
			ChannelId: channel,
			User: &pb.User{
				Id:          sender,
				DisplayName: sender,
			},
		}

		if ctcp, ok := parseCtcp(msg); ok {
			// Ignore everything but ACTION CTCP events
			if ctcp.Action != "ACTION" {
				return
			}

			b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_Action{Action: &pb.ActionEvent{
				Source: source,
				Text:   ctcp.Text,
			}}})
		} else if strings.HasPrefix(lastArg, b.cmdPrefix) {
			msgParts := strings.SplitN(lastArg, " ", 2)
			if len(msgParts) < 2 {
				msgParts = append(msgParts, "")
			}

			command := strings.TrimPrefix(msgParts[0], b.cmdPrefix)
			arg := msgParts[1]

			b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_Command{Command: &pb.CommandEvent{
				Source:  source,
				Command: command,
				Arg:     arg,
			}}})
		} else if len(lastArg) >= len(currentNick)+1 &&
			strings.HasPrefix(lastArg, currentNick) &&
			unicode.IsPunct(rune(lastArg[len(currentNick)])) &&
			lastArg[len(currentNick)+1] == ' ' {

			message := strings.TrimSpace(lastArg[len(currentNick)+1:])

			b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_Mention{Mention: &pb.MentionEvent{
				Source: source,
				Text:   message,
			}}})
		} else {
			message := lastArg

			b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_Message{Message: &pb.MessageEvent{
				Source: source,
				Text:   message,
			}}})
		}
	}
}

func (b *Backend) writeEvent(e *pb.ChatEvent) {
	b.ingestSendLock.Lock()
	defer b.ingestSendLock.Unlock()

	err := b.ingestStream.Send(e)
	if err != nil {
		b.logger.Warn().Err(err).Msg("failed to send event")
	}
}

func (b *Backend) handleIngest(ctx context.Context) error {
	for {
		msg, err := b.ingestStream.Recv()
		if err != nil {
			return err
		}

		switch v := msg.Inner.(type) {
		case *pb.ChatRequest_PerformAction:
			for _, line := range splitLines(v.PerformAction.Text) {
				innerErr := b.writeIRCMessage(&irc.Message{
					Command: "PRIVMSG",
					Params: []string{
						v.PerformAction.ChannelId,
						fmt.Sprintf("\x01ACTION %s\x01", line),
					},
				}, msg)
				if err == nil {
					err = innerErr
				}
			}
		case *pb.ChatRequest_PerformPrivateAction:
			for _, line := range splitLines(v.PerformPrivateAction.Text) {
				innerErr := b.writeIRCMessage(&irc.Message{
					Command: "PRIVMSG",
					Params: []string{
						v.PerformPrivateAction.UserId,
						fmt.Sprintf("\x01ACTION %s\x01", line),
					},
				}, msg)
				if err == nil {
					err = innerErr
				}
			}
		case *pb.ChatRequest_SendMessage:
			for _, line := range splitLines(v.SendMessage.Text) {
				innerErr := b.writeIRCMessage(&irc.Message{
					Command: "PRIVMSG",
					Params:  []string{v.SendMessage.ChannelId, line},
				}, msg)
				if err == nil {
					err = innerErr
				}
			}
		case *pb.ChatRequest_SendPrivateMessage:
			for _, line := range splitLines(v.SendPrivateMessage.Text) {
				innerErr := b.writeIRCMessage(&irc.Message{
					Command: "PRIVMSG",
					Params:  []string{v.SendPrivateMessage.UserId, line},
				}, msg)
				if err == nil {
					err = innerErr
				}
			}
		case *pb.ChatRequest_JoinChannel:
			err = b.writeIRCMessage(&irc.Message{
				Command: "JOIN",
				Params:  []string{v.JoinChannel.ChannelName},
			}, msg)
		case *pb.ChatRequest_LeaveChannel:
			err = b.writeIRCMessage(&irc.Message{
				Command: "PART",
				Params:  []string{v.LeaveChannel.ChannelId},
			}, msg)
		case *pb.ChatRequest_UpdateChannelInfo:
			err = b.writeIRCMessage(&irc.Message{
				Command: "TOPIC",
				Params:  []string{v.UpdateChannelInfo.ChannelId, v.UpdateChannelInfo.Topic},
			}, msg)
		default:
			b.logger.Warn().Msgf("unknown msg type: %T", msg.Inner)
		}

		if err != nil {
			return err
		}
	}
}

func (b *Backend) runGrpc(ctx context.Context) error {
	var err error

	for {
		// TODO: this is an ugly place for this. It also means that calling Run
		// multiple times will break and cause race conditions. This shouldn't
		// happen in practice, but it's good to remember.
		b.ingestSendLock.Lock()
		b.grpc, err = newGRPCClient(b.seabirdHost, b.seabirdToken)
		if err != nil {
			b.ingestSendLock.Unlock()
			b.logger.Warn().Err(err).Msg("got error while connecting to gRPC")
			continue
		}

		b.ingestStream, err = b.grpc.IngestEvents(ctx)
		if err != nil {
			b.ingestSendLock.Unlock()
			b.logger.Warn().Err(err).Msg("got error while calling ingest events")
			continue
		}
		b.ingestSendLock.Unlock()

		// Send the first hello event
		err = b.ingestStream.Send(&pb.ChatEvent{
			Inner: &pb.ChatEvent_Hello{
				Hello: &pb.HelloChatEvent{
					BackendInfo: &pb.Backend{
						Type: "irc",
						Id:   b.id,
					},
				},
			},
		})
		if err != nil {
			b.logger.Warn().Err(err).Msg("got error while sending hello event")
			continue
		}

		// Send any events we need in order to update the state of core. Note
		// that we separately acquire the RLock so no changes can come in
		// between the ListChannels and GetChannel calls.
		b.irc.Tracker.RLock()
		channels := b.irc.Tracker.ListChannels()
		for _, channelName := range channels {
			if channel := b.irc.Tracker.GetChannel(channelName); channel != nil {
				err = b.ingestStream.Send(&pb.ChatEvent{
					Inner: &pb.ChatEvent_JoinChannel{
						JoinChannel: &pb.JoinChannelChatEvent{
							ChannelId:   channel.Name,
							DisplayName: channel.Name,
							Topic:       channel.Topic,
						},
					},
				})
				if err != nil {
					break
				}
			}
		}
		b.irc.Tracker.RUnlock()
		if err != nil {
			b.logger.Warn().Err(err).Msg("got error while sending initial state")
			continue
		}

		err = b.handleIngest(ctx)
		if err != nil {
			b.logger.Warn().Err(err).Msg("got error while handling chat ingest")
			continue
		}
	}
}

func (b *Backend) Run() error {
	errGroup, ctx := errgroup.WithContext(context.Background())

	errGroup.Go(func() error { return b.runGrpc(ctx) })
	errGroup.Go(func() error { return b.irc.RunContext(ctx) })

	return errGroup.Wait()
}
