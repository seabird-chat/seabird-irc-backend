package seabird_irc

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/seabird-chat/seabird-irc-backend/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func newGRPCClient(host, token string) (pb.ChatIngestClient, error) {
	newCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url, err := url.Parse(host)
	if err != nil {
		return nil, err
	}

	var insecure bool

	port := url.Port()

	switch url.Scheme {
	case "http":
		insecure = true
		if port == "" {
			port = "80"
		}
	case "https":
		if port == "" {
			port = "443"
		}
	default:
		return nil, errors.New("unknown grpc scheme")
	}

	var opt grpc.DialOption
	if insecure {
		opt = grpc.WithInsecure()
	} else {
		certPool, err := x509.SystemCertPool()
		if err != nil {
			return nil, err
		}

		opt = grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(certPool, ""))
	}

	conn, err := grpc.DialContext(newCtx, fmt.Sprintf("%s:%s", url.Hostname(), port),
		opt,
		grpc.WithPerRPCCredentials(grpcTokenAuth{
			Token:    token,
			Insecure: insecure,
		}),
		grpc.WithBlock())

	return pb.NewChatIngestClient(conn), err
}

var _ credentials.PerRPCCredentials = (*grpcTokenAuth)(nil)

type grpcTokenAuth struct {
	Token    string
	Insecure bool
}

func (a grpcTokenAuth) GetRequestMetadata(ctx context.Context, in ...string) (map[string]string, error) {
	return map[string]string{
		"Authorization": "Bearer " + a.Token,
	}, nil
}

func (a grpcTokenAuth) RequireTransportSecurity() bool {
	return !a.Insecure
}
