//go:build !with_utls

package group

import (
	"context"
	"crypto/tls"
	"net"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/ntp"
)

// Builds without uTLS retain the native sing-box TLS/CA and time settings.
func newSmartProbeTLS(ctx context.Context, s *Smart, conn net.Conn, serverName, _ string) (net.Conn, error) {
	client := tls.Client(conn, &tls.Config{ServerName: serverName, RootCAs: adapter.RootPoolFromContext(s.ctx), Time: ntp.TimeFuncFromContext(s.ctx), NextProtos: []string{"http/1.1"}})
	if err := client.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return client, nil
}
