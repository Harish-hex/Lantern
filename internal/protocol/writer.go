package protocol

import (
	"fmt"
	"net"
)

// WritePacket sends a complete Lantern packet over conn:
//
//	32-byte header  |  payload  |  4-byte CRC-32 of payload
//
// The header's PayloadLen is automatically set to len(payload).
func WritePacket(conn net.Conn, hdr Header, payload []byte) error {
	hdr.PayloadLen = uint32(len(payload))

	// Build the complete wire bytes
	packet := BuildPacket(hdr, payload)

	// Write in a single call to minimise partial-write issues on TCP
	n, err := conn.Write(packet)
	if err != nil {
		return fmt.Errorf("write packet: %w", err)
	}
	if n != len(packet) {
		return fmt.Errorf("short write: wrote %d of %d bytes", n, len(packet))
	}
	return nil
}

// BuildPacket assembles header + payload + CRC into a single byte slice.
// Useful for testing without a real connection.
func BuildPacket(hdr Header, payload []byte) []byte {
	hdr.PayloadLen = uint32(len(payload))
	headerBytes := MarshalHeader(hdr)
	crc := ComputeCRC32(payload)
	crcBytes := EncodeCRC32(crc)

	packet := make([]byte, 0, HeaderSize+len(payload)+ChecksumSize)
	packet = append(packet, headerBytes...)
	packet = append(packet, payload...)
	packet = append(packet, crcBytes...)
	return packet
}
