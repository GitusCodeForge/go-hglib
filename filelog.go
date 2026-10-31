package hglib

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

// BlobObject is one revision of a file. Data is the file content with
// Mercurial's copy metadata header removed; Meta and CopySource expose that
// header. Id is the filelog node, which is distinct from a changelog node.
type BlobObject struct {
	Id         string
	Data       []byte
	Path       string
	Flags      string
	Mode       int
	Meta       map[string]string
	CopySource string
	CopyRev    string

	rawData []byte
}

func (b *BlobObject) Type() HgObjectType { return FILERev }
func (b *BlobObject) ObjectId() string   { return b.Id }
func (b *BlobObject) RawData() []byte    { return b.Data }

func (b *BlobObject) String() string {
	return fmt.Sprintf("Blob{%s,%d}", b.Id, len(b.Data))
}

var fileMetaMarker = []byte{1, '\n'}

// FilterFileMetadata strips the optional metadata header from filelog
// revision data. Mercurial packs metadata as
//
//	\1\n
//	copy: <source path>
//	copyrev: <node>
//	\1\n
//	<content>
//
// A file whose own content begins with the marker is stored with an empty
// metadata block, so this function never mistakes content for metadata.
func FilterFileMetadata(data []byte) ([]byte, map[string]string) {
	if !bytes.HasPrefix(data, fileMetaMarker) {
		return data, nil
	}
	rel := bytes.Index(data[len(fileMetaMarker):], fileMetaMarker)
	if rel < 0 {
		// Malformed or a plain file that happens to start with the marker.
		return data, nil
	}
	start := len(fileMetaMarker)
	end := start + rel
	metaText := data[start:end]
	content := data[end+len(fileMetaMarker):]
	meta := make(map[string]string)
	for _, line := range bytes.Split(metaText, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		k, v, ok := bytes.Cut(line, []byte{':', ' '})
		if !ok {
			continue
		}
		meta[string(k)] = string(v)
	}
	return content, meta
}

// PackFileMetadata adds a metadata header. It is the inverse of
// FilterFileMetadata and is useful when a caller writes filelog data itself.
func PackFileMetadata(meta map[string]string, content []byte) []byte {
	if len(meta) == 0 && !bytes.HasPrefix(content, fileMetaMarker) {
		return content
	}
	var b bytes.Buffer
	b.Write(fileMetaMarker)
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(meta[k])
		b.WriteByte('\n')
	}
	b.Write(fileMetaMarker)
	b.Write(content)
	return b.Bytes()
}

// OpenFilelog opens the revlog that stores the history of a repository file.
// Filelogs are cached per repository handle.
func (r *LocalHgRepository) OpenFilelog(filePath string) (*Revlog, error) {
	filePath = normalizeManifestPath(filePath)
	if filePath == "" {
		return nil, ErrObjectNotFound
	}
	r.filelogsMu.Lock()
	if r.filelogs == nil {
		r.filelogs = make(map[string]*Revlog)
	}
	if rl, ok := r.filelogs[filePath]; ok {
		r.filelogsMu.Unlock()
		return rl, nil
	}
	r.filelogsMu.Unlock()

	logical := filelogLogicalPath(filePath)
	encoded := EncodeStorePath(logical, r.DotEncode)
	full := localPath(r.StorePath, encoded)
	rl, err := OpenRevlog(full)
	if err != nil {
		// Narrow repositories and some internal stores keep metadata-only
		// filelogs under meta/ instead of data/.
		metaLogical := "meta/" + filePath + ".i"
		metaFull := localPath(r.StorePath, EncodeStorePath(metaLogical, r.DotEncode))
		if _, statErr := os.Stat(metaFull); statErr == nil {
			rl, err = OpenRevlog(metaFull)
		}
	}
	if err != nil {
		return nil, err
	}

	r.filelogsMu.Lock()
	if existing, ok := r.filelogs[filePath]; ok {
		r.filelogsMu.Unlock()
		rl.Close()
		return existing, nil
	}
	r.filelogs[filePath] = rl
	r.filelogsMu.Unlock()
	return rl, nil
}

// HasFilelog reports whether a repository file has a filelog on disk.
func (r *LocalHgRepository) HasFilelog(filePath string) bool {
	_, err := r.OpenFilelog(filePath)
	return err == nil
}

// ReadFilelogRevision returns the raw revision data (including a metadata
// header, if any) for one filelog node.
func (r *LocalHgRepository) ReadFilelogRevision(filePath string, node string) ([]byte, error) {
	rl, err := r.OpenFilelog(filePath)
	if err != nil {
		return nil, err
	}
	return rl.RawRevisionByNode(node)
}

func (r *LocalHgRepository) readBlob(path, node, flags string, mode int) (*BlobObject, error) {
	if flags == "t" {
		return nil, fmt.Errorf("%s is a tree manifest entry, not a file", path)
	}
	raw, err := r.ReadFilelogRevision(path, node)
	if err != nil {
		return nil, err
	}
	content, meta := FilterFileMetadata(raw)
	res := &BlobObject{
		Id:      node,
		Data:    content,
		Path:    path,
		Flags:   flags,
		Mode:    mode,
		Meta:    meta,
		rawData: raw,
	}
	if meta != nil {
		res.CopySource = meta["copy"]
		res.CopyRev = meta["copyrev"]
	}
	return res, nil
}

// ReadBlob reads one filelog revision by node id, without looking at a
// manifest. The path is needed because Mercurial keeps one revlog per file.
func (r *LocalHgRepository) ReadBlob(filePath string, node string) (*BlobObject, error) {
	filePath = normalizeManifestPath(filePath)
	rl, err := r.OpenFilelog(filePath)
	if err != nil {
		return nil, err
	}
	rev, ok := rl.RevOfNode(node)
	if !ok {
		rev, err = rl.RevOfPrefix(node)
		if err != nil {
			return nil, err
		}
	}
	// The manifest flag is not part of the filelog. Callers that have a
	// manifest should use GetFile, which supplies it.
	return r.readBlob(filePath, rl.Entries[rev].Node, "", HG_MODE_NORMAL_FILE)
}

// GetFile returns the content of a file at a changeset.
func (r *LocalHgRepository) GetFile(c *CommitObject, filePath string) (*BlobObject, error) {
	m, err := r.GetManifest(c)
	if err != nil {
		return nil, err
	}
	filePath = normalizeManifestPath(filePath)
	item, ok := m.Get(filePath)
	if !ok {
		return nil, ErrObjectNotFound
	}
	return r.readBlob(item.Path, item.Node, item.Flags, item.Mode)
}

// GetFileContent is GetFile for callers that only need the bytes.
func (r *LocalHgRepository) GetFileContent(c *CommitObject, filePath string) ([]byte, error) {
	b, err := r.GetFile(c, filePath)
	if err != nil {
		return nil, err
	}
	return b.Data, nil
}

// GetAllTrackedFiles returns the paths known to the store's fncache, or, when
// the repository has no fncache, walks the data directory. The result is
// sorted and repository-root-relative.
func (r *LocalHgRepository) GetAllTrackedFiles() ([]string, error) {
	fncache := localPath(r.StorePath, "fncache")
	if data, err := os.ReadFile(fncache); err == nil {
		res := make([]string, 0, 64)
		seen := make(map[string]bool)
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "data/") || !strings.HasSuffix(line, ".i") {
				continue
			}
			p := line[len("data/") : len(line)-len(".i")]
			if !seen[p] {
				seen[p] = true
				res = append(res, p)
			}
		}
		if len(res) > 0 {
			return res, nil
		}
	}
	// Fall back to walking the data directory, decoding each revlog name.
	// Hashed names cannot be decoded; they are skipped because without fncache
	// the original path is not recoverable from them.
	root := localPath(r.StorePath, "data")
	var res []string
	err := walkRevlogIndexes(root, func(rel string) {
		// Hashed names cannot be decoded back to a repository path.
		if strings.HasPrefix(rel, "dh/") {
			return
		}
		decoded, ok := DecodeStorePath("data/" + rel)
		if !ok {
			return
		}
		if strings.HasPrefix(decoded, "data/") && strings.HasSuffix(decoded, ".i") {
			res = append(res, decoded[len("data/"):len(decoded)-len(".i")])
		}
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func walkRevlogIndexes(root string, fn func(rel string)) error {
	var walk func(dir, prefix string) error
	walk = func(dir, prefix string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, e := range entries {
			name := e.Name()
			rel := name
			if prefix != "" {
				rel = prefix + "/" + name
			}
			if e.IsDir() {
				if err := walk(localPath(dir, name), rel); err != nil {
					return err
				}
				continue
			}
			if strings.HasSuffix(name, ".i") {
				fn(rel)
			}
		}
		return nil
	}
	return walk(root, "")
}

// DecodeStorePath reverses EncodeStorePath for reversible (non-hashed) names.
// It returns false for names that cannot be decoded, such as hashed names
// under dh/.
func DecodeStorePath(p string) (string, bool) {
	if strings.HasPrefix(p, "dh/") {
		return "", false
	}
	parts := strings.Split(p, "/")
	for i, part := range parts {
		decoded, ok := decodeStoreName(part)
		if !ok {
			return "", false
		}
		parts[i] = decoded
	}
	return decodedir(strings.Join(parts, "/")), true
}

func decodeStoreName(s string) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '~' {
			if i+2 >= len(s) {
				return "", false
			}
			h1, ok1 := hexValue(s[i+1])
			h2, ok2 := hexValue(s[i+2])
			if !ok1 || !ok2 {
				return "", false
			}
			b.WriteByte(h1<<4 | h2)
			i += 2
			continue
		}
		if c == '_' {
			if i+1 >= len(s) {
				return "", false
			}
			i++
			if s[i] == '_' {
				b.WriteByte('_')
			} else if s[i] >= 'a' && s[i] <= 'z' {
				b.WriteByte(s[i] - 'a' + 'A')
			} else {
				return "", false
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String(), true
}

func decodedir(path string) string {
	path = strings.ReplaceAll(path, ".hg.hg/", ".hg/")
	path = strings.ReplaceAll(path, ".i.hg/", ".i/")
	path = strings.ReplaceAll(path, ".d.hg/", ".d/")
	return path
}

func hexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
