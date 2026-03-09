package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/crc32"
)

// ----- CRC-32 (per-chunk integrity) -----

// ComputeCRC32 returns the IEEE CRC-32 checksum of data.
func ComputeCRC32(data []byte) uint32 {
	return crc32.ChecksumIEEE(data)
}

// VerifyChecksum checks that ComputeCRC32(data) == expected.
func VerifyChecksum(data []byte, expected uint32) bool {
	return ComputeCRC32(data) == expected
}

// EncodeCRC32 writes a uint32 CRC into a 4-byte big-endian slice.
func EncodeCRC32(crc uint32) []byte {
	buf := make([]byte, ChecksumSize)
	binary.BigEndian.PutUint32(buf, crc)
	return buf
}

// DecodeCRC32 reads a uint32 from a 4-byte big-endian slice.
func DecodeCRC32(data []byte) uint32 {
	return binary.BigEndian.Uint32(data)
}

// ----- SHA-256 (per-file integrity) -----

// SHA256Hasher accumulates data for an incremental SHA-256 hash.
type SHA256Hasher struct {
	h hash.Hash
}

// NewSHA256Hasher returns a new incremental hasher.
func NewSHA256Hasher() *SHA256Hasher {
	return &SHA256Hasher{h: sha256.New()}
}

// Update feeds more data into the running hash.
func (s *SHA256Hasher) Update(data []byte) {
	s.h.Write(data) // sha256.Write never returns an error
}

// Finalize returns the hex-encoded hash prefixed with "sha256:".
func (s *SHA256Hasher) Finalize() string {
	return fmt.Sprintf("sha256:%s", hex.EncodeToString(s.h.Sum(nil)))
}

// Reset clears the hasher for reuse.
func (s *SHA256Hasher) Reset() {
	s.h.Reset()
}
