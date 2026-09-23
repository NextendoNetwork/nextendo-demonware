package main

// UDP relay for Crash Team Racing, modelled on the ACNH server's relay.go.
//
// The server introduces peers to each other (nat.go, 0x0A -> 0x0B) but does nothing when
// the hole punch fails, and for some pairs it always fails: measured on production, the
// address each client puts in its introduction request matches what we observe
// (mismatch=false), so the introduction is correct and the failures are genuine NAT
// incompatibility. Those players simply cannot reach each other directly.
//
// bdCommonAddr, the 67-byte hostAddr blob the game exchanges, already has room for this:
//
//	slot 0      local address   ip[4] + port u16 little-endian
//	slots 1..9  RELAY addresses, empty slots carry the sentinel 0.255.0.255:0
//	slot 10     public address
//	byte 66     NAT type
//
// Filling slot 1 with a relay endpoint is what the protocol expects.
//
// Demux: a single shared port cannot tell which peer a packet is meant for, so one port is
// allocated per (host, joiner) PAIR. That works because the session list is rewritten for
// each requester individually, so every joiner can be handed its own relay port.
//
// Only the joiner's copy is rewritten. The host keeps sending to whatever address it
// learned, and the relay forwards with itself as the source, so the host's replies come
// back to the relay on their own. The host side of the introduction (the 0x0B packet) is
// HMAC-protected and must not be touched.

import (
	"encoding/binary"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// relayFor limits WHICH joiners get a relay address written into their copy of a host's
// bdCommonAddr. Empty means nobody, so turning the ports on alone changes nothing for
// anyone; "*" means everyone. It exists so the relay can be proven on one account before
// it is allowed near the ~69% of rooms that already connect directly.
func relayFor(joinerPID uint64) bool {
	list := strings.TrimSpace(envOr("CTR_RELAY_PIDS", ""))
	if list == "" {
		return false
	}
	if list == "*" {
		return true
	}
	for _, f := range strings.Split(list, ",") {
		if n, err := strconv.ParseUint(strings.TrimSpace(f), 10, 64); err == nil && n == joinerPID {
			return true
		}
	}
	return false
}

const (
	relayAddrLen  = 6  // ip[4] + port u16
	relaySlotLow  = 1  // first relay slot in bdCommonAddr
	relaySlotHigh = 9  // last relay slot
	commonAddrLen = 67 // local + 9 relay + public + nat type
)

// emptySlot is what the game writes into an unused relay slot.
var emptySlot = [4]byte{0, 255, 0, 255}

type relayPair struct {
	port   int
	host   *net.UDPAddr // the host's public endpoint, from its own hostAddr
	joiner *net.UDPAddr // learned from the first packet that is not the host
	conn   *net.UDPConn
	last   time.Time
}

type relayTable struct {
	mu     sync.Mutex
	pairs  map[string]*relayPair // "hostPID:joinerPID" -> pair
	byPort map[int]*relayPair
	low    int
	high   int
	next   int
	ip     net.IP
}

var relay = &relayTable{pairs: map[string]*relayPair{}, byPort: map[int]*relayPair{}}

func relayEnabled() bool { return relay.high >= relay.low && relay.low > 0 }

func startCTRRelay(low, high int, host string) {
	ip := net.ParseIP(host).To4()
	if ip == nil || low <= 0 || high < low {
		log.Printf("[CTR Relay] disabled (host=%q ports %d-%d)", host, low, high)
		return
	}
	relay.mu.Lock()
	relay.low, relay.high, relay.next, relay.ip = low, high, low, ip
	relay.mu.Unlock()
	log.Printf("[CTR Relay] enabled on %s:%d-%d", ip, low, high)
	go relay.reap()
}

// parseAddrSlot reads one bdCommonAddr slot. The port is little-endian, as in nat.go.
func parseAddrSlot(b []byte) *net.UDPAddr {
	if len(b) < relayAddrLen {
		return nil
	}
	if b[0] == emptySlot[0] && b[1] == emptySlot[1] && b[2] == emptySlot[2] && b[3] == emptySlot[3] {
		return nil
	}
	port := int(binary.LittleEndian.Uint16(b[4:6]))
	if port == 0 {
		return nil
	}
	return &net.UDPAddr{IP: net.IPv4(b[0], b[1], b[2], b[3]), Port: port}
}

func writeAddrSlot(b []byte, a *net.UDPAddr) {
	ip := a.IP.To4()
	copy(b[0:4], ip)
	binary.LittleEndian.PutUint16(b[4:6], uint16(a.Port))
}

// hostPublicAddr returns the public endpoint the host advertised (slot 10).
func hostPublicAddr(hostAddr []byte) *net.UDPAddr {
	if len(hostAddr) < commonAddrLen {
		return nil
	}
	return parseAddrSlot(hostAddr[60:66])
}

// withRelay returns a copy of hostAddr with a relay endpoint written into the first free
// relay slot, allocating a port for this (host, joiner) pair. The original is returned
// unchanged when the relay is off or the blob is not a bdCommonAddr.
func withRelay(hostAddr []byte, hostPID, joinerPID uint64) []byte {
	if !relayEnabled() || len(hostAddr) < commonAddrLen || hostPID == 0 || joinerPID == 0 || hostPID == joinerPID {
		return hostAddr
	}
	if !relayFor(joinerPID) {
		return hostAddr
	}
	dst := hostPublicAddr(hostAddr)
	if dst == nil {
		return hostAddr
	}
	p := relay.pairFor(hostPID, joinerPID, dst)
	if p == nil {
		return hostAddr
	}
	out := make([]byte, len(hostAddr))
	copy(out, hostAddr)
	for slot := relaySlotLow; slot <= relaySlotHigh; slot++ {
		off := slot * relayAddrLen
		if parseAddrSlot(out[off:off+relayAddrLen]) != nil {
			continue // already occupied
		}
		writeAddrSlot(out[off:off+relayAddrLen], &net.UDPAddr{IP: relay.ip, Port: p.port})
		log.Printf("[CTR Relay] offered :%d to pid=%d for host=%d (slot %d)", p.port, joinerPID, hostPID, slot)
		return out
	}
	return hostAddr
}

func pairKey(hostPID, joinerPID uint64) string {
	return string(binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint64(nil, hostPID), joinerPID))
}

// pairFor returns the relay pair for these two players, opening a socket on first use.
func (t *relayTable) pairFor(hostPID, joinerPID uint64, host *net.UDPAddr) *relayPair {
	key := pairKey(hostPID, joinerPID)
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.pairs[key]; ok {
		p.host, p.last = host, time.Now()
		return p
	}
	for tries := 0; tries <= t.high-t.low; tries++ {
		port := t.next
		t.next++
		if t.next > t.high {
			t.next = t.low
		}
		if _, busy := t.byPort[port]; busy {
			continue
		}
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
		if err != nil {
			continue
		}
		p := &relayPair{port: port, host: host, conn: conn, last: time.Now()}
		t.pairs[key], t.byPort[port] = p, p
		go p.serve()
		log.Printf("[CTR Relay] pair %d<->%d on :%d (host %s)", hostPID, joinerPID, port, host)
		return p
	}
	log.Printf("[CTR Relay] no free port for %d<->%d", hostPID, joinerPID)
	return nil
}

// serve forwards in both directions. Anything from the host goes to the joiner; anything
// else is taken to be the joiner (learned on its first packet) and goes to the host.
func (p *relayPair) serve() {
	buf := make([]byte, 2048)
	for {
		n, src, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		relay.mu.Lock()
		host, joiner := p.host, p.joiner
		fromHost := host != nil && src.IP.Equal(host.IP) && src.Port == host.Port
		if !fromHost && (joiner == nil || !src.IP.Equal(joiner.IP) || src.Port != joiner.Port) {
			p.joiner, joiner = src, src
			log.Printf("[CTR Relay] :%d FIRST PACKET from %s (%d bytes) — the game DOES use relay slots",
				p.port, src, n)
		}
		p.last = time.Now()
		relay.mu.Unlock()

		var to *net.UDPAddr
		if fromHost {
			to = joiner
		} else {
			to = host
		}
		if to == nil {
			continue
		}
		_, _ = p.conn.WriteToUDP(buf[:n], to)
	}
}

// reap closes pairs that have gone quiet so their ports can be reused.
func (t *relayTable) reap() {
	for range time.Tick(30 * time.Second) {
		cutoff := time.Now().Add(-2 * time.Minute)
		t.mu.Lock()
		for key, p := range t.pairs {
			if p.last.After(cutoff) {
				continue
			}
			_ = p.conn.Close()
			delete(t.pairs, key)
			delete(t.byPort, p.port)
		}
		t.mu.Unlock()
	}
}
