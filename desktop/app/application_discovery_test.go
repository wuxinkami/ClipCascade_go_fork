package app

import (
	"net"
	"testing"
)

func TestIsUsableDiscoveredIPv4(t *testing.T) {
	orig := appLocalUsableIPv4Networks
	appLocalUsableIPv4Networks = func() []*net.IPNet {
		_, lan, _ := net.ParseCIDR("192.168.0.0/24")
		return []*net.IPNet{lan}
	}
	t.Cleanup(func() { appLocalUsableIPv4Networks = orig })

	if !isUsableDiscoveredIPv4(net.ParseIP("192.168.0.95")) {
		t.Fatal("192.168.0.95 should be accepted as usable discovered IPv4")
	}
	if isUsableDiscoveredIPv4(net.ParseIP("172.30.5.1")) {
		t.Fatal("172.30.5.1 should be rejected when it is outside usable local LANs")
	}
	if isUsableDiscoveredIPv4(net.ParseIP("127.0.0.1")) {
		t.Fatal("127.0.0.1 should be rejected")
	}
}
