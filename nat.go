package main

// Sondes NAT Demonware sur UDP 3074 (hotes stun.{us,eu,jp,au}.demonware.net).
// Ce n'est pas du STUN RFC 5389 : trois octets d'en-tete bruts « type | version
// | bourrage », puis des champs bruts. Format repris du serveur STUN de
// project-bo4/shield-development (meme generation du SDK Demonware) et confirme
// par les sondes de la console :
//
//	1e 03 00       type 30, decouverte d'IP  -> 31 | 02 | 00 | ip[4] | port u16
//	14 02 00 00    type 20, decouverte NAT   -> 21 | 02 | 00 | ip[4] | port u16 | ipServeur[4] | portServeur u16
//
// L'IP s'ecrit en ordre reseau, le port en u16 petit-boutiste. Sans ces
// reponses le type de NAT reste inconnu et le jeu ne propose que des parties
// locales.
//
// Traversee NAT (paquets de 29 octets, format documente par
// Protarium-Network/bo2-wiiu-demonware) :
//
//	type | u16 version | id[10] | hmac[4] | adresseSource[6] | adresseDest[6]
//
// 0x0A : un joueur qui rejoint demande a etre presente a l'hote. On renvoie le
// paquet OCTET POUR OCTET a la destination avec le type 0x0B (INTRO) ; le HMAC
// couvre les adresses et seul le demandeur le verifie, donc rien d'autre ne doit
// changer. L'hote repond 0x0C directement au demandeur, ce qui ouvre la
// connexion P2P. 0x0E : keepalive, sans reponse.

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
	natSeen   = map[string]int{} // signature de paquet -> occurrences (journal borne)
	natIntros atomic.Int64       // presentations relayees, pour le tableau de bord
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
				log.Printf("[D3 NAT] INTRO %s -> %s (id=%X)", addr, to, buf[3:13])
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
