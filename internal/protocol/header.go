// Package protocol defines the Lantern wire protocol: a 32-byte fixed header,
// chunked payloads, and per-chunk CRC-32 trailers.
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// HeaderSize is the fixed size of every Lantern packet header.
	HeaderSize = 32

	// ProtocolVersion is the current wire format version.
	ProtocolVersion byte = 0x01

	// ChecksumSize is the trailing CRC-32 (4 bytes) appended to every packet.
	ChecksumSize = 4
)

// MagicBytes identifies Lantern traffic on the wire.
var MagicBytes = [4]byte{'L', 'T', 'R', 'N'}

// Header is the 32-byte fixed header that prefixes every Lantern packet.
//
//	Offset  Size  Field
//	  0       4   Magic ("LTRN")
//	  4       1   Version
//	  5       1   MsgType
//	  6       1   Flags
//	  7       1   Reserved
//	  8       4   PayloadLen (big-endian uint32)
//	 12       4   Sequence   (big-endian uint32)
//	 16      16   SessionID  (UUID, raw bytes)
type Header struct {
	Magic      [4]byte
	Version    byte
	MsgType    byte
	Flags      byte
	Reserved   byte
	PayloadLen uint32
	Sequence   uint32
	SessionID  [16]byte
}

// MarshalHeader serialises a Header into exactly 32 bytes (big-endian).
func MarshalHeader(h Header) []byte {
	buf := make([]byte, HeaderSize)
	copy(buf[0:4], h.Magic[:])
	buf[4] = h.Version
	buf[5] = h.MsgType
	buf[6] = h.Flags
	buf[7] = h.Reserved
	binary.BigEndian.PutUint32(buf[8:12], h.PayloadLen)
	binary.BigEndian.PutUint32(buf[12:16], h.Sequence)
	copy(buf[16:32], h.SessionID[:])
	return buf
}

// UnmarshalHeader deserialises 32 bytes into a Header.
// Returns an error if the magic or version fields are invalid.
func UnmarshalHeader(data []byte) (Header, error) {
	if len(data) < HeaderSize {
		return Header{}, fmt.Errorf("header too short: got %d bytes, need %d", len(data), HeaderSize)
	}

	var h Header
	copy(h.Magic[:], data[0:4])
	if h.Magic != MagicBytes {
		return Header{}, errors.New("invalid magic bytes: not a Lantern packet")
	}

	h.Version = data[4]
	if h.Version != ProtocolVersion {
		return Header{}, fmt.Errorf("unsupported protocol version: %d (expected %d)", h.Version, ProtocolVersion)
	}

	h.MsgType = data[5]
	h.Flags = data[6]
	h.Reserved = data[7]
	h.PayloadLen = binary.BigEndian.Uint32(data[8:12])
	h.Sequence = binary.BigEndian.Uint32(data[12:16])
	copy(h.SessionID[:], data[16:32])
	return h, nil
}

// NewHeader creates a Header with the magic and version fields pre-filled.
func NewHeader(msgType byte, flags byte, payloadLen uint32, seq uint32, sessionID [16]byte) Header {
	return Header{
		Magic:      MagicBytes,
		Version:    ProtocolVersion,
		MsgType:    msgType,
		Flags:      flags,
		PayloadLen: payloadLen,
		Sequence:   seq,
		SessionID:  sessionID,
	}
}

// HasFlag checks whether a specific flag bit is set.
func (h Header) HasFlag(flag byte) bool {
	return h.Flags&flag != 0
}
