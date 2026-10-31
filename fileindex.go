package hglib

import (
	"os"
	"sort"
	"strings"
)

// The fileindex-v1 store (used by version 2 repositories) replaces fncache
// with a compact, tree-structured index of every file path known to the store.
// Only the list and meta files are needed to enumerate paths; the tree file is
// an acceleration structure for path lookup and is not read here.
const (
	fileIndexDocketSize = 60
	fileIndexMarker     = "fileindex-v1"
	fileIndexMetaSize   = 8
	fileIndexMaxBytes   = 1 << 30
)

// readFileIndexPaths enumerates the paths recorded by a fileindex-v1 docket.
// The boolean is false when the index is absent or cannot be understood, in
// which case the caller should fall back to walking the data directory.
func (r *LocalHgRepository) readFileIndexPaths() ([]string, bool) {
	docket, err := os.ReadFile(localPath(r.StorePath, "fileindex"))
	if err != nil || len(docket) < fileIndexDocketSize {
		return nil, false
	}
	if string(docket[:len(fileIndexMarker)]) != fileIndexMarker {
		return nil, false
	}
	listSize := uint64(readUint32BE(docket[12:16]))
	metaSize := uint64(readUint32BE(docket[16:20]))
	listID := string(docket[24:32])
	metaID := string(docket[32:40])
	if metaSize == 0 {
		return []string{}, true
	}
	if listSize > fileIndexMaxBytes || metaSize > fileIndexMaxBytes {
		return nil, false
	}
	listPath := localPath(r.StorePath, "fileindex-list."+listID)
	list, err := os.ReadFile(listPath)
	if err != nil || uint64(len(list)) < listSize {
		return nil, false
	}
	metaPath := localPath(r.StorePath, "fileindex-meta."+metaID)
	meta, err := os.ReadFile(metaPath)
	if err != nil || uint64(len(meta)) < metaSize {
		return nil, false
	}
	list = list[:int(listSize)]
	meta = meta[:int(metaSize)]
	if len(meta)%fileIndexMetaSize != 0 {
		return nil, false
	}

	// Entry zero is the root and carries no path.
	count := len(meta) / fileIndexMetaSize
	res := make([]string, 0, count)
	seen := make(map[string]bool, count)
	for i := 1; i < count; i++ {
		entry := meta[i*fileIndexMetaSize : (i+1)*fileIndexMetaSize]
		offset := uint64(readUint32BE(entry[0:4]))
		length := uint64(readUint16BE(entry[4:6]))
		if offset+length > uint64(len(list)) {
			return nil, false
		}
		p := string(list[offset : offset+length])
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		res = append(res, p)
	}
	sort.Strings(res)
	return res, true
}

// DecodePlainStorePath reverses plainEncodeStorePath. It is only needed for
// repositories that use the experimental plain encoding without a file index.
func DecodePlainStorePath(p string) (string, bool) {
	if !strings.HasPrefix(p, "data/") {
		return p, true
	}
	return "data/" + strings.ReplaceAll(p[len("data/"):], "_/", "/"), true
}
