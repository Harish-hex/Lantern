package protocol

import (
	"fmt"
	"io"
	"net"
)

// ReadPacket reads one complete Lantern packet from conn:
//
//	32-byte header  →  PayloadLen bytes  →  4-byte CRC-32 trailer
//
// It returns the parsed header, raw payload, and the trailing CRC.
func ReadPacket(conn net.Conn) (Header, []byte, uint32, error) {
	// 1. Read 32-byte header
	headerBuf := make([]byte, HeaderSize)
	if _, err := io.ReadFull(conn, headerBuf); err != nil {
		return Header{}, nil, 0, fmt.Errorf("read header: %w", err)
	}

	hdr, err := UnmarshalHeader(headerBuf)
	if err != nil {
		return Header{}, nil, 0, fmt.Errorf("unmarshal header: %w", err)
	}

	// 2. Read payload (may be zero-length for control messages)
	var payload []byte
	if hdr.PayloadLen > 0 {
		payload = make([]byte, hdr.PayloadLen)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return Header{}, nil, 0, fmt.Errorf("read payload (%d bytes): %w", hdr.PayloadLen, err)
		}
	}

	// 3. Read 4-byte CRC trailer
	crcBuf := make([]byte, ChecksumSize)
	if _, err := io.ReadFull(conn, crcBuf); err != nil {
		return Header{}, nil, 0, fmt.Errorf("read CRC trailer: %w", err)
	}
	crc := DecodeCRC32(crcBuf)

	return hdr, payload, crc, nil
}
