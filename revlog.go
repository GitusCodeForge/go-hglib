package hglib

import (
	"bytes"
	"compress/bzip2"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Revlog global header flags. The low sixteen bits of the header word are the
// revlog version; the rest are these flags.
const (
	revlogFlagInlineData   = 1 << 16
	revlogFlagGeneralDelta = 1 << 17
	revlogFlagFilelogMeta  = 1 << 18
	revlogFlagDeltaInfo    = 1 << 19
)

const (
	revlogV0        = 0
	revlogV1        = 1
	revlogV2        = 0xDEAD
	changelogV2     = 0xD34D
	revlogV0Size    = 76
	revlogV1Size    = 64
	revlogV2Size    = 96
	maxDeltaChain   = 1 << 20
	cacheEntryLimit = 256
)

var (
	ErrRevlogNotFound         = errors.New("mercurial revlog not found")
	ErrRevNotFound            = errors.New("mercurial revision not found")
	ErrAmbiguousNode          = errors.New("ambiguous mercurial node prefix")
	ErrUnsupportedRevlog      = errors.New("unsupported mercurial revlog format")
	ErrUnsupportedCompression = errors.New("unsupported mercurial revlog compression")
	ErrNotRevlogV1            = errors.New("not a revlog version 1 index")
)

// RevlogEntry is one record of a revlog index. Revlog indexes are append-only:
// revision numbers are positions in this slice and never change.
type RevlogEntry struct {
	// Offset is the offset of the revision's chunk in the data area. For an
	// inline revlog, the data area is interleaved with the index entries.
	Offset int64
	// IndexOffset is where this entry itself lives in the index file.
	IndexOffset int64
	Flags       uint16
	// CompressedLen is the number of bytes the chunk occupies on disk.
	CompressedLen int32
	// UncompressedLen is the size of the chunk before compression. It is the
	// size of the delta, not of the reconstructed revision, for a delta.
	UncompressedLen int32
	// BaseRev is the revision this revision is a delta against. A fulltext
	// revision has BaseRev == its own revision number.
	BaseRev int32
	LinkRev int32
	P1      int32
	P2      int32
	Node    string
}

// IsFullText reports whether the entry stores a complete revision instead of
// a patch against another revision. It needs the entry's own revision number,
// which the entry alone cannot carry.
func (e RevlogEntry) IsFullText(rev int) bool {
	return e.BaseRev == int32(rev)
}

// Revlog is a parsed Mercurial revlog. Changelog, manifest and every filelog
// use this same format.
type Revlog struct {
	IndexPath     string
	DataPath      string
	FormatVersion int
	FormatFlags   uint32
	Inline        bool
	GeneralDelta  bool
	EntrySize     int
	Entries       []RevlogEntry

	indexData []byte
	dataFile  *os.File
	nodeMap   map[string]int

	cacheMu sync.Mutex
	cache   map[int][]byte
	order   []int
}

// OpenRevlogOrEmpty is OpenRevlog, but a missing index yields an empty revlog
// instead of an error. Mercurial creates the changelog and manifest lazily, so
// a freshly initialized repository has neither file.
func OpenRevlogOrEmpty(indexPath string) (*Revlog, error) {
	r, err := OpenRevlog(indexPath)
	if err == nil {
		return r, nil
	}
	if errors.Is(err, ErrRevlogNotFound) {
		return &Revlog{
			IndexPath:     indexPath,
			DataPath:      strings.TrimSuffix(indexPath, ".i") + ".d",
			FormatVersion: revlogV1,
			EntrySize:     revlogV1Size,
			nodeMap:       make(map[string]int),
			cache:         make(map[int][]byte),
		}, nil
	}
	return nil, err
}

// OpenRevlog reads and parses a revlog index. The index is kept in memory;
// data for a non-inline revlog stays on disk and is read on demand.
func OpenRevlog(indexPath string) (*Revlog, error) {
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrRevlogNotFound, indexPath)
		}
		return nil, err
	}
	r := &Revlog{
		IndexPath: indexPath,
		EntrySize: revlogV1Size,
		indexData: data,
		nodeMap:   make(map[string]int),
		cache:     make(map[int][]byte),
	}
	if len(data) == 0 {
		// An empty revlog has no header word at all. Mercurial treats it as
		// the default version.
		r.FormatVersion = revlogV1
		r.DataPath = strings.TrimSuffix(indexPath, ".i") + ".d"
		return r, nil
	}
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: %s is shorter than its header", ErrUnsupportedRevlog, indexPath)
	}
	header := readUint32BE(data[:4])
	r.FormatVersion = int(header & 0xFFFF)
	r.FormatFlags = header & 0xFFFF0000

	switch r.FormatVersion {
	case revlogV1:
		r.EntrySize = revlogV1Size
		r.Inline = r.FormatFlags&revlogFlagInlineData != 0
		r.GeneralDelta = r.FormatFlags&revlogFlagGeneralDelta != 0
	case revlogV0:
		// Version 0 predates both flags. Its data always lives in a separate
		// .d file, and its entries are 76 bytes wide.
		r.EntrySize = revlogV0Size
		r.Inline = false
		r.GeneralDelta = false
	case revlogV2, changelogV2:
		return nil, fmt.Errorf("%w: revlog version %#x", ErrUnsupportedRevlog, r.FormatVersion)
	default:
		return nil, fmt.Errorf("%w: revlog version %#x", ErrUnsupportedRevlog, r.FormatVersion)
	}

	if r.FormatVersion == revlogV0 {
		err = r.parseV0Index()
		r.DataPath = strings.TrimSuffix(indexPath, ".i") + ".d"
	} else if r.Inline {
		err = r.parseInlineIndex()
		r.DataPath = indexPath
	} else {
		err = r.parseSplitIndex()
		r.DataPath = strings.TrimSuffix(indexPath, ".i") + ".d"
	}
	if err != nil {
		return nil, err
	}
	if !r.Inline && len(r.Entries) > 0 {
		f, err := os.Open(r.DataPath)
		if err != nil {
			return nil, err
		}
		r.dataFile = f
	}
	return r, nil
}

// parseV0Index parses the original revlog index. Version 0 entries are 76
// bytes wide and store parent node ids instead of parent revision numbers:
//
//	4 bytes  offset in the .d file
//	4 bytes  compressed length
//	4 bytes  base revision of the delta chain
//	4 bytes  link revision
//	20 bytes first parent node
//	20 bytes second parent node
//	20 bytes this revision's node
//
// There is no uncompressed-length field, so entries use -1 to mean "unknown".
func (r *Revlog) parseV0Index() error {
	if len(r.indexData) == 4 {
		return nil
	}
	if len(r.indexData)%r.EntrySize != 0 {
		return fmt.Errorf("%w: %s has a partial version 0 index entry", ErrUnsupportedRevlog, r.IndexPath)
	}
	n := len(r.indexData) / r.EntrySize
	r.Entries = make([]RevlogEntry, 0, n)
	for i := 0; i < n; i++ {
		pos := i * r.EntrySize
		b := r.indexData[pos : pos+r.EntrySize]
		e := RevlogEntry{
			Offset:          int64(readUint32BE(b[0:4])),
			IndexOffset:     int64(pos),
			Flags:           0,
			CompressedLen:   readInt32BE(b[4:8]),
			UncompressedLen: -1,
			BaseRev:         readInt32BE(b[8:12]),
			LinkRev:         readInt32BE(b[12:16]),
			Node:            bytesToHex(b[56:76]),
			P1:              -1,
			P2:              -1,
		}
		if e.CompressedLen < 0 {
			return fmt.Errorf("%w: negative compressed length in %s", ErrUnsupportedRevlog, r.IndexPath)
		}
		if rev, ok := r.nodeMap[bytesToHex(b[16:36])]; ok {
			e.P1 = int32(rev)
		}
		if rev, ok := r.nodeMap[bytesToHex(b[36:56])]; ok {
			e.P2 = int32(rev)
		}
		r.Entries = append(r.Entries, e)
		if _, ok := r.nodeMap[e.Node]; !ok {
			r.nodeMap[e.Node] = i
		}
	}
	return nil
}

func (r *Revlog) parseSplitIndex() error {
	if len(r.indexData) == 4 {
		// A header word with no entries.
		return nil
	}
	if len(r.indexData)%r.EntrySize != 0 {
		return fmt.Errorf("%w: %s has a partial index entry", ErrUnsupportedRevlog, r.IndexPath)
	}
	n := len(r.indexData) / r.EntrySize
	r.Entries = make([]RevlogEntry, 0, n)
	for i := 0; i < n; i++ {
		pos := int64(i * r.EntrySize)
		e, err := parseRevlogEntry(r.indexData, pos, i == 0)
		if err != nil {
			return err
		}
		r.Entries = append(r.Entries, e)
		if _, ok := r.nodeMap[e.Node]; !ok {
			r.nodeMap[e.Node] = i
		}
	}
	return nil
}

func (r *Revlog) parseInlineIndex() error {
	if len(r.indexData) == 4 {
		// A header word with no entries.
		return nil
	}
	pos := int64(0)
	first := true
	for pos+int64(r.EntrySize) <= int64(len(r.indexData)) {
		e, err := parseRevlogEntry(r.indexData, pos, first)
		if err != nil {
			return err
		}
		first = false
		r.Entries = append(r.Entries, e)
		if _, ok := r.nodeMap[e.Node]; !ok {
			r.nodeMap[e.Node] = len(r.Entries) - 1
		}
		next := pos + int64(r.EntrySize) + int64(e.CompressedLen)
		if next <= pos {
			return fmt.Errorf("%w: %s has a non-positive inline entry", ErrUnsupportedRevlog, r.IndexPath)
		}
		pos = next
	}
	if pos != int64(len(r.indexData)) {
		return fmt.Errorf("%w: %s has trailing bytes after its inline entries", ErrUnsupportedRevlog, r.IndexPath)
	}
	return nil
}

func parseRevlogEntry(data []byte, pos int64, first bool) (RevlogEntry, error) {
	if pos < 0 || pos+revlogV1Size > int64(len(data)) {
		return RevlogEntry{}, io.ErrUnexpectedEOF
	}
	b := data[pos : pos+revlogV1Size]
	// The first eight bytes are "six bytes of offset, two bytes of flags".
	// For revision zero the first four bytes are taken by the revlog global
	// header instead, so only the last four are meaningful; revision zero's
	// data offset is always zero.
	var q uint64
	if first {
		q = uint64(readUint32BE(b[4:8]))
	} else {
		q = readUint64BE(b[0:8])
	}
	e := RevlogEntry{
		Offset:          int64(q >> 16),
		IndexOffset:     pos,
		Flags:           uint16(q & 0xFFFF),
		CompressedLen:   readInt32BE(b[8:12]),
		UncompressedLen: readInt32BE(b[12:16]),
		BaseRev:         readInt32BE(b[16:20]),
		LinkRev:         readInt32BE(b[20:24]),
		P1:              readInt32BE(b[24:28]),
		P2:              readInt32BE(b[28:32]),
		Node:            bytesToHex(b[32:52]),
	}
	if e.CompressedLen < 0 || e.UncompressedLen < 0 || e.Offset < 0 {
		return RevlogEntry{}, fmt.Errorf("%w: negative field in revlog entry", ErrUnsupportedRevlog)
	}
	return e, nil
}

// Close releases the data file of a non-inline revlog.
func (r *Revlog) Close() error {
	r.cacheMu.Lock()
	r.cache = nil
	r.order = nil
	r.cacheMu.Unlock()
	if r.dataFile != nil {
		err := r.dataFile.Close()
		r.dataFile = nil
		return err
	}
	return nil
}

// Len returns the number of revisions in the revlog.
func (r *Revlog) Len() int {
	return len(r.Entries)
}

// Entry returns the index entry for rev.
func (r *Revlog) Entry(rev int) (*RevlogEntry, error) {
	if rev < 0 || rev >= len(r.Entries) {
		return nil, ErrRevNotFound
	}
	return &r.Entries[rev], nil
}

// Node returns the hexadecimal node id of rev.
func (r *Revlog) Node(rev int) (string, error) {
	if rev < 0 || rev >= len(r.Entries) {
		return "", ErrRevNotFound
	}
	return r.Entries[rev].Node, nil
}

// RevOfNode resolves a full node id. It returns the revision number and
// whether it exists.
func (r *Revlog) RevOfNode(node string) (int, bool) {
	i, ok := r.nodeMap[strings.ToLower(node)]
	return i, ok
}

// RevOfPrefix resolves a node id or a unique prefix of one.
func (r *Revlog) RevOfPrefix(prefix string) (int, error) {
	prefix = strings.ToLower(prefix)
	if prefix == "" {
		return 0, ErrInvalidNode
	}
	if len(prefix) == 40 {
		if i, ok := r.nodeMap[prefix]; ok {
			return i, nil
		}
		return 0, ErrRevNotFound
	}
	if !IsValidNodePrefix(prefix) {
		return 0, ErrInvalidNode
	}
	found := -1
	for i := range r.Entries {
		if strings.HasPrefix(r.Entries[i].Node, prefix) {
			if found >= 0 {
				return 0, ErrAmbiguousNode
			}
			found = i
		}
	}
	if found < 0 {
		return 0, ErrRevNotFound
	}
	return found, nil
}

// ParentRevs returns the revision numbers of the two parents, or -1.
func (r *Revlog) ParentRevs(rev int) (int, int) {
	if rev < 0 || rev >= len(r.Entries) {
		return -1, -1
	}
	return int(r.Entries[rev].P1), int(r.Entries[rev].P2)
}

// ParentNodes returns the node ids of the two parents, using the null node
// where a parent is absent.
func (r *Revlog) ParentNodes(rev int) (string, string) {
	p1, p2 := r.ParentRevs(rev)
	var n1, n2 string
	if p1 >= 0 && p1 < len(r.Entries) {
		n1 = r.Entries[p1].Node
	} else {
		n1 = nullNodeHex
	}
	if p2 >= 0 && p2 < len(r.Entries) {
		n2 = r.Entries[p2].Node
	} else {
		n2 = nullNodeHex
	}
	return n1, n2
}

// LinkRev returns the changelog revision that first introduced this revision.
func (r *Revlog) LinkRev(rev int) int {
	if rev < 0 || rev >= len(r.Entries) {
		return -1
	}
	return int(r.Entries[rev].LinkRev)
}

func (r *Revlog) compressedChunk(rev int) ([]byte, error) {
	if rev < 0 || rev >= len(r.Entries) {
		return nil, ErrRevNotFound
	}
	e := &r.Entries[rev]
	if r.Inline {
		start := e.IndexOffset + int64(r.EntrySize)
		end := start + int64(e.CompressedLen)
		if end > int64(len(r.indexData)) {
			return nil, io.ErrUnexpectedEOF
		}
		return r.indexData[start:end], nil
	}
	if r.dataFile == nil {
		return nil, fmt.Errorf("%w: no data file for %s", ErrRevlogNotFound, r.IndexPath)
	}
	buf := make([]byte, e.CompressedLen)
	n, err := r.dataFile.ReadAt(buf, e.Offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if n != len(buf) {
		return nil, io.ErrUnexpectedEOF
	}
	return buf, nil
}

var (
	zstdOnce    sync.Once
	zstdDecoder *zstd.Decoder
)

func zstdDecompress(data []byte) ([]byte, error) {
	zstdOnce.Do(func() {
		// A Decoder may be used by multiple goroutines.
		zstdDecoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0))
	})
	if zstdDecoder == nil {
		return nil, ErrUnsupportedCompression
	}
	return zstdDecoder.DecodeAll(data, nil)
}

func decompressChunk(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	switch data[0] {
	case 'x':
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		return io.ReadAll(zr)
	case 0:
		// The chunk is stored as-is and happens to start with a NUL byte.
		return data, nil
	case 'u':
		// Uncompressed, with a one-byte marker.
		return data[1:], nil
	case 0x28:
		// zstd frame magic is 28 B5 2F FD.
		return zstdDecompress(data)
	case 'B':
		if len(data) > 1 && data[1] == 'Z' {
			return io.ReadAll(bzip2.NewReader(bytes.NewReader(data)))
		}
	}
	return nil, fmt.Errorf("%w: header %#x", ErrUnsupportedCompression, data[0])
}

// applyMPatch applies one Mercurial delta. A delta is a sequence of
// (start, end, length, bytes) hunks, all big-endian.
func applyMPatch(base []byte, delta []byte) ([]byte, error) {
	if len(delta) == 0 {
		return append([]byte(nil), base...), nil
	}
	// A delta against an empty base is still a normal delta whose first hunk
	// replaces [0, 0). Mercurial has a shortcut for this case; the general
	// path below produces the same result.
	out := make([]byte, 0, len(base))
	last := 0
	pos := 0
	for pos < len(delta) {
		if pos+12 > len(delta) {
			return nil, ErrInvalidDelta
		}
		start := int(readInt32BE(delta[pos : pos+4]))
		end := int(readInt32BE(delta[pos+4 : pos+8]))
		l := int(readInt32BE(delta[pos+8 : pos+12]))
		pos += 12
		if start < 0 || end < start || l < 0 || last > start || end > len(base) {
			return nil, ErrDeltaOutOfRange
		}
		if pos+l > len(delta) {
			return nil, ErrInvalidDelta
		}
		out = append(out, base[last:start]...)
		out = append(out, delta[pos:pos+l]...)
		pos += l
		last = end
	}
	if last > len(base) {
		return nil, ErrDeltaOutOfRange
	}
	out = append(out, base[last:]...)
	return out, nil
}

func (r *Revlog) cacheGet(rev int) ([]byte, bool) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.cache == nil {
		return nil, false
	}
	v, ok := r.cache[rev]
	return v, ok
}

func (r *Revlog) cachePut(rev int, data []byte) {
	// Very large revisions would push everything else out; they are cheaper to
	// recompute than to keep around.
	if len(data) > 8*1024*1024 {
		return
	}
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if r.cache == nil {
		return
	}
	if _, ok := r.cache[rev]; ok {
		r.cache[rev] = data
		return
	}
	r.cache[rev] = data
	r.order = append(r.order, rev)
	for len(r.order) > cacheEntryLimit {
		old := r.order[0]
		r.order = r.order[1:]
		delete(r.cache, old)
	}
}

func (r *Revlog) decompressedChunk(rev int) ([]byte, error) {
	raw, err := r.compressedChunk(rev)
	if err != nil {
		return nil, err
	}
	out, err := decompressChunk(raw)
	if err != nil {
		return nil, err
	}
	e := &r.Entries[rev]
	// UncompressedLen is the size of the full reconstructed revision, not of
	// the stored chunk, so it only describes this chunk when the revision is
	// a fulltext.
	if e.BaseRev == int32(rev) && e.UncompressedLen >= 0 && len(out) != int(e.UncompressedLen) {
		return nil, fmt.Errorf("revlog %s revision %d: decompressed size is %d, %d expected",
			r.IndexPath, rev, len(out), e.UncompressedLen)
	}
	return out, nil
}

// RawRevision reconstructs the revision's stored text: the changelog entry,
// the manifest text, or the filelog data including any metadata header.
func (r *Revlog) RawRevision(rev int) ([]byte, error) {
	if rev < 0 || rev >= len(r.Entries) {
		return nil, ErrRevNotFound
	}
	if data, ok := r.cacheGet(rev); ok {
		return data, nil
	}
	if !r.GeneralDelta {
		return r.rawRevisionNonGeneralDelta(rev)
	}
	chain := make([]int, 0, 8)
	cur := rev
	for {
		if cur < 0 || cur >= len(r.Entries) {
			return nil, ErrRevNotFound
		}
		chain = append(chain, cur)
		e := &r.Entries[cur]
		if e.BaseRev == int32(cur) {
			break
		}
		base := int(e.BaseRev)
		if base < 0 || base >= len(r.Entries) {
			return nil, fmt.Errorf("revlog %s revision %d: delta base %d is out of range",
				r.IndexPath, cur, base)
		}
		if len(chain) > maxDeltaChain {
			return nil, fmt.Errorf("revlog %s revision %d: delta chain is too long", r.IndexPath, rev)
		}
		cur = base
	}

	baseRev := chain[len(chain)-1]
	var data []byte
	if cached, ok := r.cacheGet(baseRev); ok {
		data = cached
	} else {
		var err error
		data, err = r.decompressedChunk(baseRev)
		if err != nil {
			return nil, err
		}
		r.cachePut(baseRev, data)
	}
	for i := len(chain) - 2; i >= 0; i-- {
		delta, err := r.decompressedChunk(chain[i])
		if err != nil {
			return nil, err
		}
		data, err = applyMPatch(data, delta)
		if err != nil {
			return nil, fmt.Errorf("revlog %s revision %d: %w", r.IndexPath, chain[i], err)
		}
		r.cachePut(chain[i], data)
	}
	if err := r.checkRevisionSize(rev, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (r *Revlog) checkRevisionSize(rev int, data []byte) error {
	e := &r.Entries[rev]
	if e.UncompressedLen >= 0 && len(data) != int(e.UncompressedLen) {
		return fmt.Errorf("revlog %s revision %d: reconstructed size is %d, %d expected",
			r.IndexPath, rev, len(data), e.UncompressedLen)
	}
	return nil
}

// rawRevisionNonGeneralDelta resolves a chain in a revlog without generaldelta.
// In that layout the base field names the first revision of the chain, not the
// immediate delta base: the first revision is a fulltext and every following
// revision is a delta against its predecessor.
func (r *Revlog) rawRevisionNonGeneralDelta(rev int) ([]byte, error) {
	e := &r.Entries[rev]
	base := int(e.BaseRev)
	if base < 0 || base > rev {
		return nil, fmt.Errorf("revlog %s revision %d: delta base %d is out of range",
			r.IndexPath, rev, base)
	}
	if int(r.Entries[base].BaseRev) != base {
		return nil, fmt.Errorf("revlog %s revision %d: chain base %d is not a fulltext",
			r.IndexPath, rev, base)
	}
	var data []byte
	var err error
	if cached, ok := r.cacheGet(base); ok {
		data = cached
	} else {
		data, err = r.decompressedChunk(base)
		if err != nil {
			return nil, err
		}
		r.cachePut(base, data)
	}
	for i := base + 1; i <= rev; i++ {
		delta, err := r.decompressedChunk(i)
		if err != nil {
			return nil, err
		}
		data, err = applyMPatch(data, delta)
		if err != nil {
			return nil, fmt.Errorf("revlog %s revision %d: %w", r.IndexPath, i, err)
		}
		r.cachePut(i, data)
	}
	if err := r.checkRevisionSize(rev, data); err != nil {
		return nil, err
	}
	return data, nil
}

// Revision is an alias for RawRevision.
func (r *Revlog) Revision(rev int) ([]byte, error) {
	return r.RawRevision(rev)
}

// RawRevisionByNode reconstructs a revision by node id.
func (r *Revlog) RawRevisionByNode(node string) ([]byte, error) {
	rev, ok := r.RevOfNode(node)
	if !ok {
		return nil, ErrRevNotFound
	}
	return r.RawRevision(rev)
}

// RevisionByNode is an alias for RawRevisionByNode.
func (r *Revlog) RevisionByNode(node string) ([]byte, error) {
	return r.RawRevisionByNode(node)
}
