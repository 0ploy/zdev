package project

import (
	"net"
	"testing"
)

func TestIsPortAvailable(t *testing.T) {
	// Test that a random high port is available
	if !isPortAvailable("tcp", "127.0.0.1:0") {
		t.Error("expected random port to be available")
	}
}

func TestIsPortAvailableDetectsOccupiedUDPPort(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket() error: %v", err)
	}
	defer conn.Close()

	if isPortAvailable("udp", conn.LocalAddr().String()) {
		t.Error("expected occupied UDP port to be unavailable")
	}
}
