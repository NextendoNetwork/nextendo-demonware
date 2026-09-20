package main

// Weekly Challenge Rifts.
//
// The game asks the lobby for two publisher files (bdStorage.getPublisherFile,
// see services.go): "challengerift_config.dat", a small protobuf (ChallengeData:
// challenge number, start, last broadcast, end, hash), and then
// "challengerift_<number>.dat" (WeeklyChallengeData, the rift itself), using the
// number the config gave. d3hack feeds the same two blobs to the game offline,
// from sd:/config/d3hack-nx/rift_data/; this serves them over the network.
//
// The blobs are captured game data and are not part of this repository. Put
// challengerift_config.dat and challengerift_NN.dat (as found in d3hack's release
// zip under config/d3hack-nx/rift_data/) in D3_RIFTDATA (default "riftdata").
// Without a challengerift_config.dat there, nothing here is served and the
// requests are answered as absent, as before.
//
// The config's times are rewritten the way d3hack does (start 0, end 2038):
// captured configs carry the week they were recorded, which is long over.

import (
	"encoding/binary"
	"errors"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var riftDir = envOr("D3_RIFTDATA", "riftdata")

// riftSettings is the "challenge_rifts" block of pubfiles.json.
type riftSettings struct {
	// Mode: "weekly" (default) advances to the next file every week,
	// "fixed" always serves Number, "random" picks a new one on every request.
	Mode   string `json:"mode"`
	Number uint32 `json:"number"`
}

const riftWeek = 7 * 24 * time.Hour

var riftDataName = regexp.MustCompile(`^challengerift_(\d+)\.dat$`)

// riftChallengeNumber is the number put in the config; the game then asks for
// that number's data file, which riftFile maps onto the files available
// (wrapping around to the first one when the number runs past the last).
func riftChallengeNumber(s riftSettings, now time.Time) uint32 {
	switch s.Mode {
	case "fixed":
		return s.Number
	case "random":
		return rand.Uint32() % 100000
	default:
		return uint32(now.Unix() / int64(riftWeek/time.Second))
	}
}

// riftDataFiles lists the numbered data files, in numeric order.
func riftDataFiles() []string {
	entries, err := os.ReadDir(riftDir)
	if err != nil {
		return nil
	}
	type nf struct {
		n    int
		path string
	}
	var found []nf
	for _, e := range entries {
		if m := riftDataName.FindStringSubmatch(strings.ToLower(e.Name())); m != nil {
			n, _ := strconv.Atoi(m[1])
			found = append(found, nf{n, filepath.Join(riftDir, e.Name())})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
	out := make([]string, len(found))
	for i, f := range found {
		out[i] = f.path
	}
	return out
}

func readVarint(b []byte, i int) (uint64, int, error) {
	v, n := binary.Uvarint(b[i:])
	if n <= 0 {
		return 0, i, errors.New("bad varint")
	}
	return v, i + n, nil
}

// patchChallengeConfig rewrites a captured ChallengeData: field 1 (number) is
// replaced, fields 2-4 (start, last broadcast, end) get never-expiring values,
// field 5 (hash) is kept.
func patchChallengeConfig(src []byte, number uint32) ([]byte, error) {
	var hash uint64
	for i := 0; i < len(src); {
		key, ni, err := readVarint(src, i)
		if err != nil {
			return nil, err
		}
		i = ni
		if key&7 != 0 {
			return nil, errors.New("unexpected wire type in challenge config")
		}
		v, ni, err := readVarint(src, i)
		if err != nil {
			return nil, err
		}
		i = ni
		if key>>3 == 5 {
			hash = v
		}
	}
	out := binary.AppendUvarint(nil, 1<<3)
	out = binary.AppendUvarint(out, uint64(number))
	for _, f := range []struct{ field, v uint64 }{{2, 0}, {3, 0}, {4, 1<<31 - 1}} {
		out = binary.AppendUvarint(out, f.field<<3)
		out = binary.AppendUvarint(out, f.v)
	}
	out = binary.AppendUvarint(out, 5<<3)
	return binary.AppendUvarint(out, hash), nil
}

// riftFile answers a publisher-file request for a Challenge Rift file.
func riftFile(name string, now time.Time) ([]byte, string, bool) {
	name = strings.ToLower(filepath.Base(name))
	if !strings.HasPrefix(name, "challengerift_") {
		return nil, "", false
	}
	cfg, err := loadPubConfig(pubConfigPath)
	if err != nil {
		return nil, "", false
	}
	if name == "challengerift_config.dat" {
		p := filepath.Join(riftDir, name)
		src, err := os.ReadFile(p)
		if err != nil {
			return nil, "", false
		}
		number := riftChallengeNumber(cfg.ChallengeRifts, now)
		out, err := patchChallengeConfig(src, number)
		if err != nil {
			log.Printf("[D3 Rift] %s: %v", p, err)
			return nil, "", false
		}
		log.Printf("[D3 Rift] config -> challenge %d (mode %q), times never expire", number, cfg.ChallengeRifts.Mode)
		return out, p, true
	}
	m := riftDataName.FindStringSubmatch(name)
	files := riftDataFiles()
	if m == nil || len(files) == 0 {
		return nil, "", false
	}
	n, _ := strconv.Atoi(m[1])
	p := files[n%len(files)]
	data, err := os.ReadFile(p)
	if err != nil {
		log.Printf("[D3 Rift] %s: %v", p, err)
		return nil, "", false
	}
	log.Printf("[D3 Rift] %s -> %s (%d of %d files)", name, filepath.Base(p), n%len(files)+1, len(files))
	return data, p, true
}
