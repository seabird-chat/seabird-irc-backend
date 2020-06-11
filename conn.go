package seabird_irc

import (
	"context"
	"sync"

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

	SeabirdHost string
	Token       string
}

type Backend struct {
	id             string
	channels       []string
	cmdPrefix      string
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
		channels:  config.Channels,
		logger:    config.Logger,
		cmdPrefix: config.CommandPrefix,
	}

	b.grpc, err = newGRPCClient(config.SeabirdHost, config.Token)
	if err != nil {
		return nil, err
	}

	b.irc, err = newIRCClient(&config, ircx.HandlerFunc(b.ircHandler))
	if err != nil {
		return nil, err
	}

	return b, nil
}

func (b *Backend) ircHandler(c *ircx.Client, m *irc.Message) {
	switch m.Command {
	case "001":
		for _, channel := range b.channels {
			_ = b.writeIRCMessage(&irc.Message{
				Command: "JOIN",
				Params:  []string{channel},
			}, nil)
		}
	case "PRIVMSG":
	case "JOIN":
		if m.Prefix.Name == b.irc.CurrentNick() {
			b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_JoinChannel{JoinChannel: &pb.JoinChannelChatEvent{
				ChannelId:   m.Params[0],
				DisplayName: m.Params[0],
			}}})
		}
	case "PART":
		if m.Prefix.Name == b.irc.CurrentNick() {
			b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_LeaveChannel{LeaveChannel: &pb.LeaveChannelChatEvent{
				ChannelId: m.Params[0],
			}}})
		}
	case "KICK":
		if m.Params[1] == b.irc.CurrentNick() {
			b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_LeaveChannel{LeaveChannel: &pb.LeaveChannelChatEvent{
				ChannelId: m.Params[0],
			}}})
		}
	case "PONG":
		b.handlePong(m.Trailing())
	case irc.RPL_TOPIC:
		b.writeEvent(&pb.ChatEvent{Inner: &pb.ChatEvent_ChangeChannel{ChangeChannel: &pb.ChangeChannelChatEvent{
			ChannelId:   m.Params[1],
			DisplayName: m.Params[1],
			Topic:       m.Trailing(),
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
		*pb.ChatRequest_SendPrivateMessage:
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
		case *pb.ChatRequest_SendMessage:
			err = b.writeIRCMessage(&irc.Message{
				Command: "PRIVMSG",
				Params:  []string{v.SendMessage.ChannelId, v.SendMessage.Text},
			}, msg)
		case *pb.ChatRequest_SendPrivateMessage:
			err = b.writeIRCMessage(&irc.Message{
				Command: "PRIVMSG",
				Params:  []string{v.SendPrivateMessage.UserId, v.SendPrivateMessage.Text},
			}, msg)
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

func (b *Backend) Run() error {
	var err error
	errGroup, ctx := errgroup.WithContext(context.Background())

	// TODO: this is an ugly place for this. It also means that calling Run
	// multiple times will break and cause race conditions. This shouldn't
	// happen in practice, but it's good to remember.
	b.ingestStream, err = b.grpc.IngestEvents(ctx)
	if err != nil {
		return err
	}

	b.ingestStream.Send(&pb.ChatEvent{
		Inner: &pb.ChatEvent_Hello{
			Hello: &pb.HelloChatEvent{
				BackendInfo: &pb.Backend{
					Type: "irc",
					Id:   b.id,
				},
			},
		},
	})

	errGroup.Go(func() error { return b.handleIngest(ctx) })
	errGroup.Go(func() error { return b.irc.RunContext(ctx) })

	return errGroup.Wait()
}
