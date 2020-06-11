package seabird_irc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"time"

	"github.com/go-irc/irc/v4"
	"github.com/go-irc/ircx"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/seabird-chat/seabird-irc-backend/pb"
)

type IRCConfig struct {
	IRCHost string
	Nick    string
	User    string
	Name    string
	Pass    string

	Logger zerolog.Logger

	SeabirdHost string
	Token       string
}

type Backend struct {
	conn    *ircx.Client
	seabird *pb.ChatIngestClient
	ingest  pb.ChatIngest_IngestEventsClient
}

func New(config IRCConfig) (*Backend, error) {
	b := &Backend{}

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

	b.conn = ircx.NewClient(c, ircx.ClientConfig{
		Nick:          config.Nick,
		User:          config.User,
		Name:          config.Name,
		Pass:          config.Pass,
		PingFrequency: 60 * time.Second,
		PingTimeout:   10 * time.Second,
		Handler:       ircx.HandlerFunc(b.ircHandler),
	})

	return b, nil
}

func (b *Backend) ircHandler(c *ircx.Client, m *irc.Message) {

}

func (b *Backend) handleIngest(ctx context.Context) error {
	return errors.New("unimplemented")
}

func (b *Backend) Run() error {
	errGroup, ctx := errgroup.WithContext(context.Background())

	errGroup.Go(func() error { return b.handleIngest(ctx) })
	errGroup.Go(func() error { return b.conn.RunContext(ctx) })

	return errGroup.Wait()
}
