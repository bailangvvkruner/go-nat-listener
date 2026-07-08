package nattraversal

import (
	"context"
	"fmt"
	"net"
)

func listenTCPContext(ctx context.Context, port int) (net.Listener, error) {
	config := net.ListenConfig{Control: reuseAddrControl}
	return config.Listen(ctx, "tcp", fmt.Sprintf(":%d", port))
}
