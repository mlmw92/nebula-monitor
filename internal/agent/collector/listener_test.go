package collector

import (
	"testing"

	gnet "github.com/shirou/gopsutil/v4/net"
)

func TestListenerProtocol(t *testing.T) {
	tests := []struct {
		name     string
		conn     gnet.ConnectionStat
		protocol string
		family   string
		ok       bool
	}{
		{
			name:     "ipv4 tcp",
			conn:     gnet.ConnectionStat{Type: 1, Laddr: gnet.Addr{IP: "0.0.0.0"}},
			protocol: "tcp",
			family:   "ipv4",
			ok:       true,
		},
		{
			name:     "ipv6 tcp",
			conn:     gnet.ConnectionStat{Type: 1, Laddr: gnet.Addr{IP: "::"}},
			protocol: "tcp6",
			family:   "ipv6",
			ok:       true,
		},
		{
			name:     "ipv4 udp",
			conn:     gnet.ConnectionStat{Type: 2, Laddr: gnet.Addr{IP: "127.0.0.1"}},
			protocol: "udp",
			family:   "ipv4",
			ok:       true,
		},
		{
			name:     "ipv6 udp",
			conn:     gnet.ConnectionStat{Type: 2, Laddr: gnet.Addr{IP: "2001:db8::1"}},
			protocol: "udp6",
			family:   "ipv6",
			ok:       true,
		},
		{
			name: "unsupported socket type",
			conn: gnet.ConnectionStat{Type: 3, Laddr: gnet.Addr{IP: "127.0.0.1"}},
		},
		{
			name: "invalid address",
			conn: gnet.ConnectionStat{Type: 1, Laddr: gnet.Addr{IP: "not-an-ip"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protocol, family, ok := listenerProtocol(tt.conn)
			if ok != tt.ok || protocol != tt.protocol || family != tt.family {
				t.Fatalf("listenerProtocol() = (%q, %q, %v), want (%q, %q, %v)", protocol, family, ok, tt.protocol, tt.family, tt.ok)
			}
		})
	}
}
