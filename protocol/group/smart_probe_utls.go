//go:build with_utls

package group

import (
	"context"
	"net"

	boxTLS "github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
)

func newSmartProbeTLS(ctx context.Context, s *Smart, conn net.Conn, serverName, fingerprint string) (net.Conn, error) {
	config, err := boxTLS.NewUTLSClient(s.ctx, s.logger, serverName, option.OutboundTLSOptions{
		Enabled: true,
		ALPN:    []string{"http/1.1"},
		UTLS:    &option.OutboundUTLSOptions{Enabled: true, Fingerprint: fingerprint},
	})
	if err != nil {
		return nil, err
	}
	return boxTLS.ClientHandshake(ctx, conn, config)
}
