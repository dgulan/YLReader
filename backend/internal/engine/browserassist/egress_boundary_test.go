//go:build integration && linux

package browserassist

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestEgressNetworkNoneBoundary(t *testing.T) {
	if os.Getenv("TEST_BROWSER_NETWORK_NONE") != "1" {
		t.Skip("requires dedicated network-none container")
	}
	interfaces, e := net.Interfaces()
	if e != nil {
		t.Fatal(e)
	}
	for _, i := range interfaces {
		if i.Flags&net.FlagLoopback == 0 {
			t.Fatal("unexpected network interface")
		}
	}
	for _, target := range []struct{ network, address string }{{"tcp", "8.8.8.8:443"}, {"tcp", "[2606:4700:4700::1111]:443"}, {"udp", "8.8.8.8:53"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		conn, e := (&net.Dialer{}).DialContext(ctx, target.network, target.address)
		cancel()
		if e == nil {
			conn.Close()
			t.Fatal("direct egress available", target.network)
		}
	}
}
