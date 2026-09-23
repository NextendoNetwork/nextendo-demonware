package main

// Demonware NAT probes on UDP 3074 (hosts stun.{us,eu,jp,au}.demonware.net).
// This is not RFC 5389 STUN: three raw header bytes "type | version |
// padding", then raw fields. Format taken from project-bo4/shield-development's
// STUN server (same Demonware SDK generation) and confirmed by the console's
// probes:
//
//	1e 03 00       type 30, IP discovery  -> 31 | 02 | 00 | ip[4] | port u16
//	14 02 00 00    type 20, NAT discovery -> 21 | 02 | 00 | ip[4] | port u16 | serverIp[4] | serverPort u16
//
// The IP is written in network order, the port as a little-endian u16. Without
// these replies the NAT type stays unknown and the game only offers local
// games.
//
// NAT traversal (29-byte packets, format documented by
// Protarium-Network/bo2-wiiu-demonware):
//
//	type | u16 version | id[10] | hmac[4] | sourceAddress[6] | destAddress[6]
//
// 0x0A: a joining player asks to be introduced to the host. We send the packet
// back BYTE FOR BYTE to the destination with type 0x0B (INTRO); the HMAC covers
// the addresses and only the requester verifies it, so nothing else may
// change. The host answers 0x0C directly to the requester, which opens the P2P
// connection. 0x0E: keepalive, no reply.

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

const natSeenMax = 4096

var (
	natSeenMu sync.Mutex
	natSeen   = map[string]int{} // packet signature -> occurrences (bounded log)
	natIntros atomic.Int64       // introductions relayed, for the dashboard
)

func serveNAT(port int) {
	pc, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Printf("[D3 NAT] udp %d unavailable: %v", port, err)
		return
	}
	serverIP := net.ParseIP(nextendoHost).To4()
	if serverIP == nil {
		log.Printf("[D3 NAT] NEXTENDO_HOST=%q is not an IPv4 address: announcing 127.0.0.1", nextendoHost)
		serverIP = net.IPv4(127, 0, 0, 1).To4()
	}
	log.Printf("[D3 NAT] listening UDP %d (announced server ip %s)", port, serverIP)

	buf := make([]byte, 2048)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil || n < 3 {
			continue
		}
		ua, ok := addr.(*net.UDPAddr)
		if !ok || ua.IP.To4() == nil {
			continue
		}
		sig := fmt.Sprintf("%X", buf[:n])
		natSeenMu.Lock()
		if len(natSeen) >= natSeenMax {
			natSeen = map[string]int{}
		}
		cnt := natSeen[sig]
		natSeen[sig] = cnt + 1
		natSeenMu.Unlock()

		if n == 29 && (buf[0] == 0x0A || buf[0] == 0x0E) {
			if buf[0] == 0x0E {
				continue
			}
			dst := buf[23:29]
			dstIP := net.IPv4(dst[0], dst[1], dst[2], dst[3])
			dstPort := int(binary.LittleEndian.Uint16(dst[4:6]))
			if dstPort == 0 || dstIP.Equal(net.IPv4(0, 255, 0, 255)) {
				log.Printf("[D3 NAT] 0x0A from %s without destination: %X", addr, buf[:n])
				continue
			}
			intro := append([]byte{0x0B}, buf[1:n]...)
			to := &net.UDPAddr{IP: dstIP, Port: dstPort}
			if _, err := pc.WriteTo(intro, to); err != nil {
				log.Printf("[D3 NAT] INTRO to %s: %v", to, err)
			} else {
				natIntros.Add(1)
				// The host answers 0x0C to sourceAddress from INSIDE the packet, not to
				// where we saw the request come from. A client that fills in its LAN
				// address there is unreachable and the join fails with "failed to connect".
				src := buf[17:23]
				srcAddr := &net.UDPAddr{IP: net.IPv4(src[0], src[1], src[2], src[3]),
					Port: int(binary.LittleEndian.Uint16(src[4:6]))}
				ua, _ := addr.(*net.UDPAddr)
				mismatch := ua == nil || !ua.IP.Equal(srcAddr.IP) || ua.Port != srcAddr.Port
				log.Printf("[D3 NAT] INTRO %s -> %s (id=%X) claimed_src=%s mismatch=%v",
					addr, to, buf[3:13], srcAddr, mismatch)
			}
			continue
		}

		resp := []byte{}
		switch buf[0] {
		case 30:
			resp = append(resp, 31, 2, 0)
			resp = append(resp, ua.IP.To4()...)
			resp = binary.LittleEndian.AppendUint16(resp, uint16(ua.Port))
		case 20:
			resp = append(resp, 21, 2, 0)
			resp = append(resp, ua.IP.To4()...)
			resp = binary.LittleEndian.AppendUint16(resp, uint16(ua.Port))
			resp = append(resp, serverIP...)
			resp = binary.LittleEndian.AppendUint16(resp, uint16(port))
		default:
			if cnt < 2 {
				log.Printf("[D3 NAT] unknown packet from %s: %s", addr, hex.EncodeToString(buf[:n]))
				if dir := runDumpDir("nat"); dir != "" {
					_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("unknown_%d.bin", connNum.Add(1))), buf[:n], 0o644)
				}
			}
			continue
		}
		if _, err := pc.WriteTo(resp, addr); err != nil {
			log.Printf("[D3 NAT] reply to %s: %v", addr, err)
		} else if cnt < 2 {
			log.Printf("[D3 NAT] %s type %d -> %X", addr, buf[0], resp)
		}
	}
}
