package cmd

import (
	"strings"
	"testing"
)

// The fence accepts the project's subnets but treats each gateway as the host
// (on Linux it is the host's bridge address), also when IPAM doesn't name it
// (review 2026-11-28, H2).
func TestSubnetsAndGateways(t *testing.T) {
	subnets, gateways := subnetsAndGateways("172.29.0.0/16=172.29.0.1 192.168.16.0/20= fd00:1::/64= junk=1.2.3.4\n")
	if strings.Join(subnets, ",") != "172.29.0.0/16,192.168.16.0/20,fd00:1::/64" {
		t.Errorf("subnets = %v", subnets)
	}
	if strings.Join(gateways, ",") != "172.29.0.1,192.168.16.1" {
		t.Errorf("gateways = %v", gateways)
	}
}
