package protocol

import (
	"bytes"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	sid := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	hdr := NewHeader(MsgChunk, FlagLastChunk, 0, 42, sid)

	if hdr.Magic != MagicBytes {
		t.Fatalf("magic = %v, want %v", hdr.Magic, MagicBytes)
	}
	if hdr.MsgType != MsgChunk {
		t.Fatalf("type = 0x%02x, want 0x%02x", hdr.MsgType, MsgChunk)
	}
	if !hdr.HasFlag(FlagLastChunk) {
		t.Fatal("expected FlagLastChunk to be set")
	}
	if hdr.Sequence != 42 {
		t.Fatalf("seq = %d, want 42", hdr.Sequence)
	}
	if hdr.SessionID != sid {
		t.Fatal("session ID mismatch")
	}
}

func TestMarshalUnmarshalHeader(t *testing.T) {
	sid := [16]byte{0xDE, 0xAD}
	orig := NewHeader(MsgFileHeader, FlagCompressed, 1024, 7, sid)
	data := MarshalHeader(orig)

	if len(data) != HeaderSize {
		t.Fatalf("marshalled len = %d, want %d", len(data), HeaderSize)
	}

	got, err := UnmarshalHeader(data)
	if err != nil {
		t.Fatalf("UnmarshalHeader: %v", err)
	}
	if got.MsgType != orig.MsgType || got.Flags != orig.Flags || got.Sequence != orig.Sequence {
		t.Fatalf("header mismatch: orig=%+v got=%+v", orig, got)
	}
}

func TestBuildPacketRoundTrip(t *testing.T) {
	sid := [16]byte{}
	hdr := NewHeader(MsgFileHeader, 0, 0, 1, sid)
	payload := []byte(`{"filename":"test.txt","size":1024}`)

	packet := BuildPacket(hdr, payload)

	// Verify we can read it back (manually parse since ReadPacket needs net.Conn)
	if len(packet) != HeaderSize+len(payload)+ChecksumSize {
		t.Fatalf("packet len = %d, want %d", len(packet), HeaderSize+len(payload)+ChecksumSize)
	}

	// Parse header
	gotHdr, err := UnmarshalHeader(packet[:HeaderSize])
	if err != nil {
		t.Fatalf("UnmarshalHeader: %v", err)
	}
	if gotHdr.MsgType != MsgFileHeader {
		t.Fatalf("type = 0x%02x, want 0x%02x", gotHdr.MsgType, MsgFileHeader)
	}
	if gotHdr.Sequence != 1 {
		t.Fatalf("seq = %d, want 1", gotHdr.Sequence)
	}
	if gotHdr.PayloadLen != uint32(len(payload)) {
		t.Fatalf("payloadLen = %d, want %d", gotHdr.PayloadLen, len(payload))
	}

	// Extract payload
	gotPayload := packet[HeaderSize : HeaderSize+gotHdr.PayloadLen]
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("payload mismatch: %q vs %q", gotPayload, payload)
	}

	// Verify CRC
	crcBytes := packet[HeaderSize+gotHdr.PayloadLen:]
	gotCRC := DecodeCRC32(crcBytes)
	expectedCRC := ComputeCRC32(payload)
	if gotCRC != expectedCRC {
		t.Fatalf("CRC mismatch: got 0x%08X, want 0x%08X", gotCRC, expectedCRC)
	}
}

func TestCRCIntegrity(t *testing.T) {
	sid := [16]byte{}
	hdr := NewHeader(MsgChunk, 0, 0, 1, sid)
	payload := []byte("hello world, this is a test payload for CRC verification")

	packet := BuildPacket(hdr, payload)

	// Extract and verify CRC is correct first
	crcOffset := HeaderSize + len(payload)
	storedCRC := DecodeCRC32(packet[crcOffset:])
	expectedCRC := ComputeCRC32(payload)
	if storedCRC != expectedCRC {
		t.Fatalf("initial CRC mismatch")
	}

	// Corrupt one byte of the payload in the packet
	packet[HeaderSize+5] ^= 0xFF

	// The corrupted payload should fail CRC verification
	corruptedPayload := packet[HeaderSize:crcOffset]
	if VerifyChecksum(corruptedPayload, storedCRC) {
		t.Fatal("expected CRC mismatch on corrupted packet")
	}
}

func TestLargePayload(t *testing.T) {
	sid := [16]byte{}
	hdr := NewHeader(MsgChunk, 0, 0, 99, sid)
	payload := make([]byte, 256*1024) // 256 KB
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	packet := BuildPacket(hdr, payload)

	// Verify header
	gotHdr, err := UnmarshalHeader(packet[:HeaderSize])
	if err != nil {
		t.Fatalf("UnmarshalHeader: %v", err)
	}
	if gotHdr.Sequence != 99 {
		t.Fatalf("seq = %d, want 99", gotHdr.Sequence)
	}
	if gotHdr.PayloadLen != uint32(len(payload)) {
		t.Fatalf("payloadLen = %d, want %d", gotHdr.PayloadLen, len(payload))
	}

	// Verify payload matches
	gotPayload := packet[HeaderSize : HeaderSize+int(gotHdr.PayloadLen)]
	if !bytes.Equal(gotPayload, payload) {
		t.Fatal("large payload mismatch")
	}

	// Verify CRC
	crcBytes := packet[HeaderSize+int(gotHdr.PayloadLen):]
	if !VerifyChecksum(payload, DecodeCRC32(crcBytes)) {
		t.Fatal("CRC verification failed for large payload")
	}
}

func TestSHA256Hasher(t *testing.T) {
	h := NewSHA256Hasher()
	h.Update([]byte("hello "))
	h.Update([]byte("world"))
	result := h.Finalize()

	// Expected SHA-256 of "hello world"
	expected := "sha256:b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if result != expected {
		t.Fatalf("sha256 = %s, want %s", result, expected)
	}
}

func TestSHA256HasherReset(t *testing.T) {
	h := NewSHA256Hasher()
	h.Update([]byte("garbage"))
	h.Reset()
	h.Update([]byte("clean"))
	result := h.Finalize()

	h2 := NewSHA256Hasher()
	h2.Update([]byte("clean"))
	expected := h2.Finalize()

	if result != expected {
		t.Fatalf("after reset: got %s, want %s", result, expected)
	}
}

func TestEmptyPayloadPacket(t *testing.T) {
	sid := [16]byte{}
	hdr := NewHeader(MsgControl, 0, 0, 0, sid)
	payload := []byte{}

	packet := BuildPacket(hdr, payload)
	if len(packet) != HeaderSize+ChecksumSize {
		t.Fatalf("empty payload packet len = %d, want %d", len(packet), HeaderSize+ChecksumSize)
	}

	gotHdr, err := UnmarshalHeader(packet[:HeaderSize])
	if err != nil {
		t.Fatalf("UnmarshalHeader: %v", err)
	}
	if gotHdr.PayloadLen != 0 {
		t.Fatalf("payloadLen = %d, want 0", gotHdr.PayloadLen)
	}
}

func TestInvalidMagicBytes(t *testing.T) {
	badHeader := make([]byte, HeaderSize)
	copy(badHeader[0:4], []byte("NOPE"))

	_, err := UnmarshalHeader(badHeader)
	if err == nil {
		t.Fatal("expected error for invalid magic bytes")
	}
}

func TestInvalidProtocolVersion(t *testing.T) {
	hdr := NewHeader(MsgChunk, 0, 0, 0, [16]byte{})
	data := MarshalHeader(hdr)
	data[4] = 0xFF // corrupt version

	_, err := UnmarshalHeader(data)
	if err == nil {
		t.Fatal("expected error for invalid protocol version")
	}
}
