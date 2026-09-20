package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestCTRFriendBeyondFirstTwenty(t *testing.T) {
	host := &lobbyConn{n: 90343, player: &playerID{PID: 90343}}
	guest := &lobbyConn{n: 99000, player: &playerID{PID: 99000}}
	defer func() {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		for id, s := range sessions {
			if s.ownerPID == host.pid() {
				delete(sessions, id)
			}
		}
	}()
	info := &bdWriter{}
	info.blobv(make([]byte, 67))
	info.u32(8)
	info.u32(8)
	host.onMatchMakingContext(mmCreateSession, &bdReader{b: info.b}, "ctr:full-list-test")
	for _, size := range []int{20, 80} {
		ids := &bdWriter{}
		for i := 1; i <= size; i++ {
			ids.u64(uint64(90300 + i))
		}
		reply := guest.onFriendSessions(14, &bdReader{b: ids.b}, "ctr:full-list-test")
		want := uint32(0)
		if size >= 43 {
			want = 1
		}
		if got := binary.LittleEndian.Uint32(reply[17:21]); got != want {
			t.Fatalf("%d requested friends: got %d rooms, want %d", size, got, want)
		}
	}
}

func TestCTRFriendResultUnpacksUploadedAttributes(t *testing.T) {
	host := &lobbyConn{n: 89011, player: &playerID{PID: 89011}}
	guest := &lobbyConn{n: 89012, player: &playerID{PID: 89012}}
	defer func() {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		for id, s := range sessions {
			if s.ownerPID == host.pid() {
				delete(sessions, id)
			}
		}
	}()
	attrs := &bdWriter{}
	attrs.blobv(make([]byte, 8))
	attrs.blobv(make([]byte, 16))
	attrs.strv("PARTY-Test")
	attrs.u64(host.pid())
	attrs.u64(123)
	for i := 0; i < 12; i++ {
		attrs.u32(uint32(i))
	}
	attrs.raw([]byte{0x0d, 0, 0, 0, 0})
	request := &bdWriter{}
	request.blobv(make([]byte, 67))
	request.u32(8)
	request.u32(8)
	request.blobv(attrs.b)
	request.u32(999) // request option must not leak into result fields
	host.onMatchMakingContext(mmCreateSession, &bdReader{b: request.b}, "ctr:attrs-test")
	ask := &bdWriter{}
	ask.u64(host.pid())
	result := guest.onFriendSessions(14, &bdReader{b: ask.b}, "ctr:attrs-test")
	r := &bdReader{b: result, off: 26}
	r.blob()
	r.blob()
	r.u32()
	r.u32()
	r.u32()
	if !bytes.Equal(result[r.off:], attrs.b) {
		t.Fatal("CTR attributes retain upload wrapper or request options")
	}
	securityID, err := r.blob()
	if err != nil || len(securityID) != 8 {
		t.Fatal("client cannot read first result attribute")
	}
}

func TestCTRReservedSessionLifecycle(t *testing.T) {
	host := &lobbyConn{n: 89001, player: &playerID{PID: 89001}}
	guest := &lobbyConn{n: 89002, player: &playerID{PID: 89002}}
	defer func() {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		for id, s := range sessions {
			if s.ownerPID == host.pid() {
				delete(sessions, id)
			}
		}
	}()
	reply := host.onMatchMakingContext(mmRequestSessionID, &bdReader{}, "ctr:reserve-test")
	r := &bdReader{b: reply, off: 26}
	sid, err := r.blob()
	if err != nil || len(sid) != 8 {
		t.Fatal("missing reserved session ID", err)
	}
	lookup := func() uint32 {
		w := &bdWriter{}
		w.u64(host.pid())
		b := guest.onFriendSessions(14, &bdReader{b: w.b}, "ctr:reserve-test")
		return binary.LittleEndian.Uint32(b[17:21])
	}
	if lookup() != 0 {
		t.Fatal("uninitialized reservation exposed to friends")
	}
	w := &bdWriter{}
	w.blobv(sid)
	w.blobv(make([]byte, 67))
	w.u32(1)
	w.u32(8)
	bad := guest.onMatchMakingContext(mmInitializeSession, &bdReader{b: w.b}, "ctr:reserve-test")
	if binary.LittleEndian.Uint32(bad[10:14]) == 0 {
		t.Fatal("non-owner initialized session")
	}
	good := host.onMatchMakingContext(mmInitializeSession, &bdReader{b: w.b}, "ctr:reserve-test")
	if binary.LittleEndian.Uint32(good[10:14]) != 0 || lookup() != 1 {
		t.Fatal("initialized room not discoverable")
	}
}
