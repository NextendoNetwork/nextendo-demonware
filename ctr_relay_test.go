package main

import (
	"bytes"
	"encoding/base64"
	"net"
	"testing"
)

// A real hostAddr captured from production on 2026-09-21: local 172.30.96.1:3074, nine
// empty relay slots, public 47.189.198.183:3074, NAT type 2.
const realHostAddr = "rB5gAQIMAP8A/wAAAP8A/wAAAP8A/wAAAP8A/wAAAP8A/wAAAP8A/wAAAP8A/wAAAP8A/wAAAP8A/wAAL73GtwIMAg=="

func realAddr(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(realHostAddr)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != commonAddrLen {
		t.Fatalf("hostAddr is %d bytes, want %d", len(b), commonAddrLen)
	}
	return b
}

func TestParseRealHostAddr(t *testing.T) {
	b := realAddr(t)
	local := parseAddrSlot(b[0:6])
	if local == nil || local.String() != "172.30.96.1:3074" {
		t.Fatalf("local = %v, want 172.30.96.1:3074", local)
	}
	pub := hostPublicAddr(b)
	if pub == nil || pub.String() != "47.189.198.183:3074" {
		t.Fatalf("public = %v, want 47.189.198.183:3074", pub)
	}
	for slot := relaySlotLow; slot <= relaySlotHigh; slot++ {
		if got := parseAddrSlot(b[slot*relayAddrLen : slot*relayAddrLen+relayAddrLen]); got != nil {
			t.Fatalf("relay slot %d should be empty, got %v", slot, got)
		}
	}
}

// The gate is what keeps this away from the ~69% of rooms that already connect directly.
func TestRelayGateDefaultsToNobody(t *testing.T) {
	b := realAddr(t)
	relay.low, relay.high, relay.ip = 3100, 3139, net.IPv4(1, 2, 3, 4)
	defer func() { relay.low, relay.high = 0, 0 }()

	t.Setenv("CTR_RELAY_PIDS", "")
	if got := withRelay(b, 111, 222); !bytes.Equal(got, b) {
		t.Fatal("an empty CTR_RELAY_PIDS must leave hostAddr untouched")
	}
	t.Setenv("CTR_RELAY_PIDS", "999")
	if got := withRelay(b, 111, 222); !bytes.Equal(got, b) {
		t.Fatal("a non-matching pid must leave hostAddr untouched")
	}
}

func TestRelayInjectsIntoFirstFreeSlotOnly(t *testing.T) {
	b := realAddr(t)
	relay.low, relay.high, relay.next, relay.ip = 3100, 3139, 3100, net.IPv4(203, 0, 113, 9)
	relay.pairs, relay.byPort = map[string]*relayPair{}, map[int]*relayPair{}
	defer func() {
		for _, p := range relay.pairs {
			_ = p.conn.Close()
		}
		relay.low, relay.high = 0, 0
		relay.pairs, relay.byPort = map[string]*relayPair{}, map[int]*relayPair{}
	}()

	t.Setenv("CTR_RELAY_PIDS", "222")
	got := withRelay(b, 111, 222)
	if bytes.Equal(got, b) {
		t.Fatal("a matching pid should have had a relay address written in")
	}
	if len(got) != commonAddrLen {
		t.Fatalf("length changed to %d", len(got))
	}
	// The host's own addresses must survive untouched, or the direct path is destroyed.
	if !bytes.Equal(got[0:6], b[0:6]) {
		t.Fatal("local address was modified")
	}
	if !bytes.Equal(got[60:67], b[60:67]) {
		t.Fatal("public address or NAT type was modified")
	}
	slot1 := parseAddrSlot(got[relayAddrLen : 2*relayAddrLen])
	if slot1 == nil || slot1.IP.String() != "203.0.113.9" {
		t.Fatalf("slot 1 = %v, want the relay ip", slot1)
	}
	if slot1.Port < 3100 || slot1.Port > 3139 {
		t.Fatalf("relay port %d outside the configured range", slot1.Port)
	}
	// Only slot 1 may be filled; the rest stay empty.
	for slot := relaySlotLow + 1; slot <= relaySlotHigh; slot++ {
		if parseAddrSlot(got[slot*relayAddrLen:slot*relayAddrLen+relayAddrLen]) != nil {
			t.Fatalf("slot %d should still be empty", slot)
		}
	}
}

// Asking twice for the same pair must reuse the port rather than leak a socket per request,
// since the session list is rebuilt on every friends query.
func TestRelayReusesPortForSamePair(t *testing.T) {
	b := realAddr(t)
	relay.low, relay.high, relay.next, relay.ip = 3140, 3149, 3140, net.IPv4(203, 0, 113, 9)
	relay.pairs, relay.byPort = map[string]*relayPair{}, map[int]*relayPair{}
	defer func() {
		for _, p := range relay.pairs {
			_ = p.conn.Close()
		}
		relay.low, relay.high = 0, 0
		relay.pairs, relay.byPort = map[string]*relayPair{}, map[int]*relayPair{}
	}()
	t.Setenv("CTR_RELAY_PIDS", "*")

	first := parseAddrSlot(withRelay(b, 111, 222)[relayAddrLen : 2*relayAddrLen])
	second := parseAddrSlot(withRelay(b, 111, 222)[relayAddrLen : 2*relayAddrLen])
	if first == nil || second == nil || first.Port != second.Port {
		t.Fatalf("port changed between calls: %v then %v", first, second)
	}
	if len(relay.pairs) != 1 {
		t.Fatalf("%d pairs allocated for one pairing", len(relay.pairs))
	}
}
