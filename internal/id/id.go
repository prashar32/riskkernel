// Package id generates identifiers. RiskKernel mints RFC 4122 v4 UUIDs using
// crypto/rand — dependency-free, since fewer dependencies means a more auditable
// trust surface (CLAUDE.md §10).
package id

import (
	"crypto/rand"
	"encoding/hex"
)

// NewUUID returns a random RFC 4122 version-4 UUID string. It panics only if the
// system CSPRNG fails, which is unrecoverable.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("id: crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10

	var buf [36]byte
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf[:])
}
