package seabird_irc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-irc/irc/v4"
	"github.com/go-irc/ircx"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/seabird-chat/seabird-go"
	"github.com/seabird-chat/seabird-go/pb"
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
	id           string
	channels     []string
	cmdPrefix    string
	logger       zerolog.Logger
	ircSendLock  sync.Mutex
	irc          *ircx.Client
	inner        *seabird.ChatIngestClient
	outputStream chan *pb.ChatEvent
	requestsLock sync.Mutex
	requests     map[string]*pb.ChatRequest
}

func New(config IRCConfig) (*Backend, error) {
	client, err := seabird.NewChatIngestClient(config.SeabirdHost, config.SeabirdToken)
	if err != nil {
		return nil, err
	}

	b := &Backend{
		id:           config.IRCID,
		channels:     config.Channels,
		logger:       config.Logger,
		cmdPrefix:    config.CommandPrefix,
		inner:        client,
		outputStream: make(chan *pb.ChatEvent, 10),
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
	// Note that we need to allow events to be dropped so we don't lose the
	// connection when the gRPC service is down.
	select {
	case b.outputStream <- e:
	default:
	}
}

func (b *Backend) handleIngest(ctx context.Context) {
	ingestStream, err := b.inner.IngestEvents("irc", b.id)
	if err != nil {
		b.logger.Warn().Err(err).Msg("got error while calling ingest events")
		return
	}

	// Send any events we need in order to update the state of core. Note
	// that we separately acquire the RLock so no changes can come in
	// between the ListChannels and GetChannel calls.
	b.irc.Tracker.RLock()
	channels := b.irc.Tracker.ListChannels()
	for _, channelName := range channels {
		if channel := b.irc.Tracker.GetChannel(channelName); channel != nil {
			err = ingestStream.Send(&pb.ChatEvent{
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
		return
	}

	// Loop through all events and handle them
	for {
		select {
		case event := <-b.outputStream:
			b.logger.Debug().Msgf("Sending event: %+v", event)

			err := ingestStream.Send(event)
			if err != nil {
				b.logger.Warn().Err(err).Msgf("got error while sending event: %+v", event)
				return
			}

		case msg, ok := <-ingestStream.C:
			if !ok {
				b.logger.Warn().Err(errors.New("ingest stream ended")).Msg("unexpected end of ingest stream")
				return
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
				b.logger.Warn().Err(err).Msg("failed to write irc message")
				return
			}

		case <-ctx.Done():
			return
		}
	}
}

func (b *Backend) runGrpc(ctx context.Context) error {
	for {
		b.handleIngest(ctx)

		// If the context exited, we're shutting down
		err := ctx.Err()
		if err != nil {
			return err
		}

		time.Sleep(5 * time.Second)
	}
}

func (b *Backend) Run() error {
	errGroup, ctx := errgroup.WithContext(context.Background())

	errGroup.Go(func() error { return b.runGrpc(ctx) })
	errGroup.Go(func() error { return b.irc.RunContext(ctx) })

	return errGroup.Wait()
}
