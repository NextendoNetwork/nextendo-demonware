package main

// Lobby remote tasks (bdRemoteTask), read from the NSO.
//
// Client request (encrypted message, inner type 0x86): a TYPED bdByteBuffer
// whose first byte — the service identifier — is written raw:
//
//	u8 service | 03 u8 task | typed arguments
//
// Reply (inner type 0x01, pump 0xBE96E0 -> 0xBE3FE0 -> 0xC00370):
//
//	0A u64 transaction | 08 u32 error | 03 u8 | 08 u32 numResults | 08 u32 total | results
//
// Error 0 yields results, 200 leaves the task pending, any other value makes
// it fail cleanly. Tasks are served in order (the first pending one takes the
// reply): there is no identifier to match.
//
// Type tags (readers 0xBD90E0/0xBD9170/0xBDA100/0xBDA220/0xBDA4C0):
// 01 bool, 03 u8, 08 u32, 0A u64, 10 NUL-terminated string, 13 blob (08 u32 + bytes).

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	tagBool   = 0x01
	tagU8     = 0x03
	tagI32    = 0x07
	tagU32    = 0x08
	tagU64    = 0x0A
	tagString = 0x10
	tagBlob   = 0x13

	innerTaskReply = 0x01

	svcStorage        = 10
	svcTitleUtilities = 12

	taskGetPublisherFile = 21
	taskGetServerTime    = 6
)

// Error returned for an unhandled task: nonzero and different from 200, so
// the task fails instead of timing out (a timeout drops the lobby).
const errUnhandled = 1

var transactions atomic.Uint64

// bdWriter writes a typed bdByteBuffer.
type bdWriter struct{ b []byte }

func (w *bdWriter) u8(v byte) { w.b = append(w.b, tagU8, v) }
func (w *bdWriter) u32(v uint32) {
	w.b = append(w.b, tagU32)
	w.b = binary.LittleEndian.AppendUint32(w.b, v)
}
func (w *bdWriter) u64(v uint64) {
	w.b = append(w.b, tagU64)
	w.b = binary.LittleEndian.AppendUint64(w.b, v)
}
func (w *bdWriter) blobv(p []byte) {
	w.b = append(w.b, tagBlob)
	w.u32(uint32(len(p)))
	w.b = append(w.b, p...)
}

// bdReader reads a typed bdByteBuffer (request arguments).
type bdReader struct {
	b   []byte
	off int
}

var errShort = errors.New("tampon trop court")

func (r *bdReader) tag(want byte) error {
	if r.off >= len(r.b) {
		return errShort
	}
	if r.b[r.off] != want {
		return errors.New("etiquette inattendue")
	}
	r.off++
	return nil
}

func (r *bdReader) u8() (byte, error) {
	if err := r.tag(tagU8); err != nil {
		return 0, err
	}
	if r.off >= len(r.b) {
		return 0, errShort
	}
	v := r.b[r.off]
	r.off++
	return v, nil
}

func (r *bdReader) str() (string, error) {
	if err := r.tag(tagString); err != nil {
		return "", err
	}
	end := r.off
	for end < len(r.b) && r.b[end] != 0 {
		end++
	}
	if end >= len(r.b) {
		return "", errShort
	}
	s := string(r.b[r.off:end])
	r.off = end + 1
	return s, nil
}

// taskReply builds the payload of a task reply.
func taskReply(task byte, errCode uint32, results func(w *bdWriter) uint32) []byte {
	w := &bdWriter{}
	w.u64(transactions.Add(1))
	w.u32(errCode)
	if errCode != 0 {
		return w.b
	}
	w.u8(task)
	body := &bdWriter{}
	n := uint32(0)
	if results != nil {
		n = results(body)
	}
	// 0xC005B0 only reads the total if there is at least one result.
	w.u32(n)
	if n > 0 {
		w.u32(n)
		w.b = append(w.b, body.b...)
	}
	return w.b
}

// onTask handles a decrypted request. payload = bytes after the inner type.
func (l *lobbyConn) onTask(payload []byte) {
	if len(payload) < 3 {
		l.logf("task: payload too short %X", payload)
		return
	}
	service := payload[0]
	r := &bdReader{b: payload, off: 1}
	task, err := r.u8()
	if err != nil {
		l.logf("task: service %d without a task id (%v): %X", service, err, payload)
		return
	}
	noteTask(l, service, task)

	var reply []byte
	switch {
	case service == svcTitleUtilities && task == taskGetServerTime:
		now := uint32(time.Now().Unix())
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.u32(now); return 1 })
		l.logf("task bdTitleUtilities.getServerTime -> %d", now)

	case service == svcStorage && task == taskGetPublisherFile:
		ctx, err1 := r.str()
		name, err2 := r.str()
		if err1 != nil || err2 != nil {
			l.logf("getPublisherFile: unreadable arguments: %X", payload)
			reply = taskReply(task, errUnhandled, nil)
			break
		}
		data, path := l.publisherFile(name)
		if data == nil {
			// No result rather than an error: the game treats a "missing
			// file" as a normal case.
			reply = taskReply(task, 0, nil)
			l.logf("task bdStorage.getPublisherFile ctx=%q file=%q -> MISSING", ctx, name)
			break
		}
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.blobv(data); return 1 })
		l.logf("task bdStorage.getPublisherFile ctx=%q file=%q -> %s (%d bytes)", ctx, name, path, len(data))

	case service == svcTitleUtilities && task == 9:
		reply = l.onGetUserNames(task, r)

	case service == 29 && (task == 1 || task == 4):
		if reply = l.onUserData(task, r); reply == nil {
			reply = taskReply(task, 0, nil)
		}

	case service == svcMatchMaking:
		if reply = l.onMatchMaking(task, r); reply == nil {
			reply = taskReply(task, 0, nil)
			l.logf("task matchmaking UNHANDLED task=%d args:\n%s", task, hex.Dump(payload))
		}

	default:
		// Success with no result: harmless for list requests, whereas an
		// error may mark the service unavailable on the game side.
		reply = taskReply(task, 0, nil)
		l.logf("task UNHANDLED service=%d task=%d (empty success) args:\n%s", service, task, hex.Dump(payload))
	}

	if err := l.sendEncrypted(innerTaskReply, reply); err != nil {
		l.logf("send reply: %v", err)
	}
}

// publisherFile looks the file up in the pubfiles folder, ignoring case (the
// game asks for "Config.txt", the generator writes "config.txt").
func (l *lobbyConn) publisherFile(name string) ([]byte, string) {
	if data, path, ok := riftFile(name, time.Now()); ok {
		return data, path
	}
	if data, path, ok := rotatedPubfile(name, time.Now()); ok {
		return data, path
	}
	base := filepath.Base(name)
	entries, err := os.ReadDir(l.pubDir)
	if err != nil {
		return nil, ""
	}
	candidates := []string{base}
	if alias, ok := pubFileAliases[strings.ToLower(base)]; ok {
		candidates = append(candidates, alias)
	}
	for _, want := range candidates {
		for _, e := range entries {
			if strings.EqualFold(e.Name(), want) {
				p := filepath.Join(l.pubDir, e.Name())
				if b, err := os.ReadFile(p); err == nil {
					return b, p
				}
			}
		}
	}
	return nil, ""
}

// Real names requested by the game -> names written by the generator (taken
// from d3hack's cache). Seen live: Config.txt, Seasons.txt, Blacklist.txt.
var pubFileAliases = map[string]string{
	"seasons.txt":   "seasons_config.txt",
	"blacklist.txt": "blacklist_config.txt",
}
