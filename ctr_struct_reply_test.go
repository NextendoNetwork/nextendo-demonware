package main

import (
	"encoding/binary"
	"testing"
)

// Follow the read order in CTR's bdRemoteTask::handleTaskReply and
// bdStructBufferTask::deserializeTaskReply, rather than ordinary result lists.
func TestCTRHTTPProxyStructuredEnvelope(t *testing.T) {
	body := []byte(`{"expiresIn":3600,"ABToken":"nextendo","enrollments":[]}`)
	reply := httpProxyReply(1, 200, body)
	if len(reply) < 22 || reply[0] != 0x0a || reply[9] != 0x08 || binary.LittleEndian.Uint32(reply[10:14]) != 0 || reply[14] != 3 || reply[15] != 1 {
		t.Fatal("invalid task header")
	}
	if reply[16] != 0x17 {
		t.Fatalf("CTR expects StructStart immediately after task header; got %#x (ordinary counts reject the reply)", reply[16])
	}
	if reply[17] != 8 {
		t.Fatal("missing typed structure length")
	}
	n := int(binary.LittleEndian.Uint32(reply[18:22]))
	if n != len(reply)-22 {
		t.Fatal("structure length mismatch")
	}
	// The current HTTP-proxy protobuf contains status in fields 1 and 2,
	// followed by field 3 containing the exact JSON body.
	p := reply[22:]
	if len(p) < 8 || string(p[:6]) != string([]byte{8, 0xc8, 1, 16, 0xc8, 1}) || p[6] != 0x1a {
		t.Fatal("invalid status/body fields")
	}
	if int(p[7]) != len(body) || string(p[8:]) != string(body) {
		t.Fatal("JSON body damaged")
	}
}
