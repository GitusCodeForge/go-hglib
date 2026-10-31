package hglib

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"strings"
)

// The null node is twenty zero bytes. Mercurial uses it wherever a parent
// or a tag target is absent.
const nullNodeHex = "0000000000000000000000000000000000000000"

var nullNodeBytes = make([]byte, 20)

var (
	ErrInvalidNode     = errors.New("invalid mercurial node id")
	ErrInvalidDelta    = errors.New("invalid mercurial delta")
	ErrDeltaOutOfRange = errors.New("mercurial delta refers outside the base revision")
)

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			continue
		}
		if c >= 'a' && c <= 'f' {
			continue
		}
		if c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

// IsValidNode reports whether s is a full Mercurial node id. Mercurial has
// only ever used SHA-1 nodes, so a node is forty hexadecimal characters.
func IsValidNode(s string) bool {
	return len(s) == 40 && isHexString(s)
}

// IsValidNodePrefix reports whether s could be a prefix of a node id.
func IsValidNodePrefix(s string) bool {
	return len(s) > 0 && len(s) <= 40 && isHexString(s)
}

func hexToBytes(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		s = "0" + s
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, ErrInvalidNode
	}
	return b, nil
}

func bytesToHex(b []byte) string {
	return hex.EncodeToString(b)
}

func readUint32BE(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func readUint64BE(b []byte) uint64 {
	return uint64(readUint32BE(b))<<32 | uint64(readUint32BE(b[4:]))
}

func readInt32BE(b []byte) int32 {
	return int32(readUint32BE(b))
}

// HashRevision returns the node Mercurial assigns to a revision whose raw
// text is text and whose parents are p1 and p2. This is
// mercurial.utils.storageutil.hashrevisionsha1: when p2 is null the hash is
// seeded with the SHA-1 of the null node, otherwise the two parents are
// hashed in sorted order.
func HashRevision(text []byte, p1 string, p2 string) string {
	p1b, err := hexToBytes(p1)
	if err != nil || len(p1b) != 20 {
		p1b = nullNodeBytes
	}
	p2b, err := hexToBytes(p2)
	if err != nil || len(p2b) != 20 {
		p2b = nullNodeBytes
	}
	var h [20]byte
	if strings.EqualFold(p2, nullNodeHex) {
		s := sha1.New()
		s.Write(nullNodeBytes)
		s.Write(p1b)
		s.Write(text)
		copy(h[:], s.Sum(nil))
	} else {
		a, b := p1b, p2b
		if strings.Compare(string(a), string(b)) > 0 {
			a, b = b, a
		}
		s := sha1.New()
		s.Write(a)
		s.Write(b)
		s.Write(text)
		copy(h[:], s.Sum(nil))
	}
	return hex.EncodeToString(h[:])
}

// HashRevisionBytes is HashRevision for callers that keep node ids as bytes.
func HashRevisionBytes(text []byte, p1 []byte, p2 []byte) string {
	if len(p1) != 20 {
		p1 = nullNodeBytes
	}
	if len(p2) != 20 {
		p2 = nullNodeBytes
	}
	return HashRevision(text, bytesToHex(p1), bytesToHex(p2))
}

// splitLines splits data on '\n' without allocating a trailing empty line for
// a terminating newline.
func splitLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	res := make([][]byte, 0, 16)
	for {
		i := indexByte(data, '\n')
		if i < 0 {
			res = append(res, data)
			return res
		}
		res = append(res, data[:i])
		data = data[i+1:]
		if len(data) == 0 {
			return res
		}
	}
}

func indexByte(b []byte, c byte) int {
	for i := 0; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}
