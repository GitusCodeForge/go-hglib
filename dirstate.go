package hglib

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrUnsupportedDirstateV2 = errors.New("dirstate version 2 is not supported")

// DirstateEntry is one record of .hg/dirstate. State is one of 'n' (normal),
// 'a' (added), 'r' (removed) or 'm' (merged).
type DirstateEntry struct {
	State byte
	Mode  uint32
	Size  uint32
	Mtime uint32
	Name  string
}

// Dirstate is the parsed .hg/dirstate. P1 and P2 are the working directory
// parents as hexadecimal node ids; a null parent is all zeroes.
type Dirstate struct {
	P1      string
	P2      string
	Entries []DirstateEntry
}

// ParseDirstate parses dirstate version 1. Version 2 has a different layout
// and is rejected; see ErrUnsupportedDirstateV2.
func ParseDirstate(data []byte) (*Dirstate, error) {
	if len(data) < 40 {
		if len(data) == 0 {
			return &Dirstate{P1: nullNodeHex, P2: nullNodeHex}, nil
		}
		return nil, fmt.Errorf("dirstate is shorter than its two parents")
	}
	res := &Dirstate{
		P1: bytesToHex(data[0:20]),
		P2: bytesToHex(data[20:40]),
	}
	pos := 40
	for pos < len(data) {
		if pos+17 > len(data) {
			return nil, fmt.Errorf("dirstate has a partial entry at %d", pos)
		}
		state := data[pos]
		mode := readUint32BE(data[pos+1 : pos+5])
		size := readUint32BE(data[pos+5 : pos+9])
		mtime := readUint32BE(data[pos+9 : pos+13])
		nameLen := readUint32BE(data[pos+13 : pos+17])
		pos += 17
		if uint64(pos)+uint64(nameLen) > uint64(len(data)) {
			return nil, fmt.Errorf("dirstate entry name runs past the end")
		}
		name := string(data[pos : pos+int(nameLen)])
		pos += int(nameLen)
		res.Entries = append(res.Entries, DirstateEntry{
			State: state,
			Mode:  mode,
			Size:  size,
			Mtime: mtime,
			Name:  name,
		})
	}
	return res, nil
}

// GetDirstate reads and parses .hg/dirstate.
func (r *LocalHgRepository) GetDirstate() (*Dirstate, error) {
	if hasRequirement(r.Requirements, "dirstate-v2") {
		return nil, ErrUnsupportedDirstateV2
	}
	data, err := os.ReadFile(filepath.Join(r.HgDirectoryPath, "dirstate"))
	if err != nil {
		if os.IsNotExist(err) {
			return &Dirstate{P1: nullNodeHex, P2: nullNodeHex}, nil
		}
		return nil, err
	}
	return ParseDirstate(data)
}

// GetWorkingDirParents returns the parents recorded in the dirstate.
func (r *LocalHgRepository) GetWorkingDirParents() (string, string, error) {
	d, err := r.GetDirstate()
	if err != nil {
		return "", "", err
	}
	return d.P1, d.P2, nil
}

func (d *Dirstate) IsEmptyParent() bool {
	return d == nil || d.P1 == nullNodeHex
}
