package main

// Ecoute du lobby. 3074 est le port historique du stack bd (partage avec Xbox
// Live) ; les voisins sont ouverts parce que le port reel n'est pas deductible
// du binaire. La connexion elle-meme (hello, poignee de main, canal chiffre) est
// dans handshake.go.

import (
	"log"
	"net"
	"strings"
	"sync/atomic"
)

var connNum atomic.Uint64

func startLobby() {
	dumps := runDumpDir("lobby")
	opened := 0
	for _, p := range strings.Split(lobbyPorts, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		ln, err := net.Listen("tcp", ":"+p)
		if err != nil {
			log.Printf("[D3 Lobby] port %s unavailable: %v", p, err)
			continue
		}
		opened++
		go func(ln net.Listener) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				lc := &lobbyConn{c: c, n: connNum.Add(1), dumpDir: dumps, sessDir: sessDir, pubDir: pubDir}
				go func() {
					registerConn(lc)
					defer unregisterConn(lc)
					lc.run()
				}()
			}
		}(ln)
	}
	if opened == 0 {
		log.Fatal("[D3 Lobby] no port could be opened")
	}
	log.Printf("[D3 Lobby] listening TCP %s (%d port(s), dumps=%v)", lobbyPorts, opened, dumps != "")
}
