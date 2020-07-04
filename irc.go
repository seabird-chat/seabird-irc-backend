package seabird_irc

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-irc/irc/v4"
	"github.com/go-irc/ircx"
	"github.com/seabird-chat/seabird-go/pb"
)

type CTCPMessage struct {
	Action string
	Text   string
}

func parseCtcp(msg *irc.Message) (*CTCPMessage, bool) {
	lastArg := msg.Trailing()

	if !(strings.HasPrefix(lastArg, "\x01") && strings.HasSuffix(lastArg, "\x01")) {
		return nil, false
	}

	split := strings.SplitN(strings.TrimPrefix(strings.TrimSuffix(lastArg, "\x01"), "\x01"), " ", 2)
	ret := &CTCPMessage{
		Action: split[0],
	}

	if len(split) == 2 {
		ret.Text = split[1]
	}

	return ret, true
}

func newIRCClient(config *IRCConfig, handler ircx.Handler) (*ircx.Client, error) {
	ircUrl, err := url.Parse(config.IRCHost)
	if err != nil {
		return nil, err
	}

	hostname := ircUrl.Hostname()
	port := ircUrl.Port()

	var c io.ReadWriteCloser

	switch ircUrl.Scheme {
	case "irc":
		if port == "" {
			port = "6667"
		}

		c, err = net.Dial("tcp", fmt.Sprintf("%s:%s", hostname, port))
	case "ircs":
		if port == "" {
			port = "6697"
		}

		c, err = tls.Dial("tcp", fmt.Sprintf("%s:%s", hostname, port), nil)
	case "ircs+unsafe":
		if port == "" {
			port = "6697"
		}

		c, err = tls.Dial("tcp", fmt.Sprintf("%s:%s", hostname, port), &tls.Config{
			InsecureSkipVerify: true,
		})
	default:
		return nil, fmt.Errorf("unknown irc scheme %s", ircUrl.Scheme)
	}

	if err != nil {
		return nil, err
	}

	return ircx.NewClient(c, ircx.ClientConfig{
		Nick:          config.Nick,
		User:          config.User,
		Name:          config.Name,
		Pass:          config.Pass,
		EnableTracker: true,
		PingFrequency: 60 * time.Second,
		PingTimeout:   10 * time.Second,
		//SendLimit:     1 * time.Second,
		//SendBurst:     4,
		Handler: handler,
	}), nil
}

func (b *Backend) writeIRCMessage(m *irc.Message, msg *pb.ChatRequest) error {
	b.ircSendLock.Lock()
	defer b.ircSendLock.Unlock()

	err := b.irc.WriteMessage(m)
	if err != nil {
		b.logger.Warn().Err(err).Msg("failed to write message to IRC")
	}

	if msg != nil && msg.Id != "" {
		b.requestsLock.Lock()
		b.requests[msg.Id] = msg
		b.requestsLock.Unlock()

		err = b.irc.WriteMessage(&irc.Message{
			Command: "PING",
			Params:  []string{msg.Id},
		})
		if err != nil {
			b.logger.Warn().Err(err).Msg("failed to write ping message to IRC")
		}
	}

	return err
}
