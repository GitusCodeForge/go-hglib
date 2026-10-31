package hglib

import (
	"fmt"
	"strings"
)

// Mercurial does not store Unix modes. A manifest entry has a one-letter flag:
// "" for a normal file, "x" for an executable, "l" for a symbolic link and "t"
// for a tree manifest entry. These constants expose them as Git-like modes so
// that callers do not have to special-case Mercurial.
const (
	HG_MODE_NORMAL_FILE     = 0644
	HG_MODE_EXECUTABLE_FILE = 0755
	HG_MODE_SYMBOLIC_LINK   = 0120000
	HG_MODE_TREE            = 0040000
)

// ManifestItem is one file in a manifest. Mercurial's manifest is flat: it
// lists every file in the repository, not just one directory. Directories are
// implied by the slashes in Path.
type ManifestItem struct {
	Path  string
	Node  string
	Flags string
	Mode  int
}

func (m ManifestItem) String() string {
	return fmt.Sprintf("<<[%o]%s,%s>>", m.Mode, m.Path, m.Node)
}

// ManifestObject is a parsed manifest.
type ManifestObject struct {
	Id       string
	ItemList []ManifestItem
	rawData  []byte
	index    map[string]int
}

func (m *ManifestObject) Type() HgObjectType { return MANIFEST }
func (m *ManifestObject) ObjectId() string   { return m.Id }
func (m *ManifestObject) RawData() []byte    { return m.rawData }

func (m *ManifestObject) String() string {
	return fmt.Sprintf("Manifest{%s,%d}", m.Id, len(m.ItemList))
}

func flagToMode(flag string) int {
	switch flag {
	case "x":
		return HG_MODE_EXECUTABLE_FILE
	case "l":
		return HG_MODE_SYMBOLIC_LINK
	case "t":
		return HG_MODE_TREE
	default:
		return HG_MODE_NORMAL_FILE
	}
}

func modeToFlag(mode int) string {
	switch mode {
	case HG_MODE_EXECUTABLE_FILE:
		return "x"
	case HG_MODE_SYMBOLIC_LINK:
		return "l"
	case HG_MODE_TREE:
		return "t"
	default:
		return ""
	}
}

// ParseManifest parses manifest text. Each line is "<path>\0<40 hex><flag>\n",
// with flag absent or one of "", "x", "l", "t".
func ParseManifest(node string, data []byte) (*ManifestObject, error) {
	res := &ManifestObject{
		Id:       node,
		rawData:  data,
		ItemList: make([]ManifestItem, 0, 64),
		index:    make(map[string]int),
	}
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		zero := indexByte(line, 0)
		if zero < 0 {
			return nil, fmt.Errorf("manifest %s: line without a NUL separator", node)
		}
		path := string(line[:zero])
		rest := line[zero+1:]
		if len(rest) < 40 {
			return nil, fmt.Errorf("manifest %s: short node id for %q", node, path)
		}
		nd := string(rest[:40])
		flags := ""
		if len(rest) > 40 {
			flags = string(rest[40:])
		}
		if len(flags) > 1 {
			return nil, fmt.Errorf("manifest %s: invalid flags %q for %q", node, flags, path)
		}
		item := ManifestItem{
			Path:  path,
			Node:  nd,
			Flags: flags,
			Mode:  flagToMode(flags),
		}
		res.index[path] = len(res.ItemList)
		res.ItemList = append(res.ItemList, item)
	}
	return res, nil
}

// Get returns the entry for a file path. Directories are not entries in a
// Mercurial manifest; use HasDir for those.
func (m *ManifestObject) Get(path string) (*ManifestItem, bool) {
	path = normalizeManifestPath(path)
	i, ok := m.index[path]
	if !ok {
		return nil, false
	}
	return &m.ItemList[i], true
}

// HasDir reports whether at least one manifest entry lives under dir.
func (m *ManifestObject) HasDir(dir string) bool {
	dir = normalizeManifestPath(dir)
	if dir == "" {
		return len(m.ItemList) > 0
	}
	prefix := dir + "/"
	// The manifest is sorted by path, so a binary search would work; a scan is
	// simpler and this is not a hot path.
	for i := range m.ItemList {
		if strings.HasPrefix(m.ItemList[i].Path, prefix) {
			return true
		}
	}
	return false
}

func normalizeManifestPath(p string) string {
	p = strings.Trim(p, "/")
	if p == "." {
		return ""
	}
	return p
}

// TreeObjectItem is one direct entry of a directory view of a manifest.
type TreeObjectItem struct {
	Mode  int
	Name  string
	Hash  string
	IsDir bool
}

func (t TreeObjectItem) String() string {
	return fmt.Sprintf("<<[%o]%s,%s>>", t.Mode, t.Name, t.Hash)
}

// TreeObject is a directory listing built from the flat manifest. Mercurial
// does not store directories as objects, so the Id is the manifest node and
// Path is the directory the view was taken at.
type TreeObject struct {
	Id         string
	Path       string
	ObjectList []TreeObjectItem

	manifest *ManifestObject
	commit   *CommitObject
	repo     *LocalHgRepository
}

func (t *TreeObject) Type() HgObjectType { return MANIFEST }
func (t *TreeObject) ObjectId() string   { return t.Id }
func (t *TreeObject) RawData() []byte {
	if t.manifest == nil {
		return nil
	}
	return t.manifest.RawData()
}

func (t *TreeObject) String() string {
	return fmt.Sprintf("Tree{%s,%s,%s}", t.Id, t.Path, t.ObjectList)
}

// Children returns the direct entries of dir in the manifest. Entries are in
// manifest order, which is sorted by path.
func (m *ManifestObject) Children(dir string) []TreeObjectItem {
	dir = normalizeManifestPath(dir)
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	res := make([]TreeObjectItem, 0, 8)
	seenDir := make(map[string]bool)
	for i := range m.ItemList {
		item := &m.ItemList[i]
		if prefix != "" && !strings.HasPrefix(item.Path, prefix) {
			continue
		}
		rest := item.Path[len(prefix):]
		if rest == "" {
			continue
		}
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			name := rest[:slash]
			if seenDir[name] {
				continue
			}
			seenDir[name] = true
			res = append(res, TreeObjectItem{
				Mode:  HG_MODE_TREE,
				Name:  name,
				IsDir: true,
			})
		} else {
			res = append(res, TreeObjectItem{
				Mode: item.Mode,
				Name: rest,
				Hash: item.Node,
			})
		}
	}
	return res
}

// GetManifest returns the manifest of a changeset.
func (r *LocalHgRepository) GetManifest(c *CommitObject) (*ManifestObject, error) {
	if c == nil {
		return nil, fmt.Errorf("no changeset given")
	}
	return r.GetManifestByNode(c.TreeObjId)
}

// GetManifestByNode returns a manifest by its node id.
func (r *LocalHgRepository) GetManifestByNode(node string) (*ManifestObject, error) {
	data, err := r.ManifestLog.RawRevisionByNode(node)
	if err != nil {
		return nil, err
	}
	return ParseManifest(node, data)
}

// GetTree returns a directory listing of dir in the changeset's manifest.
// dir is repository-root-relative and "" means the root.
func (r *LocalHgRepository) GetTree(c *CommitObject, dir string) (*TreeObject, error) {
	m, err := r.GetManifest(c)
	if err != nil {
		return nil, err
	}
	dir = normalizeManifestPath(dir)
	if dir != "" && !m.HasDir(dir) {
		return nil, ErrObjectNotFound
	}
	items := m.Children(dir)
	return &TreeObject{
		Id:         m.Id,
		Path:       dir,
		ObjectList: items,
		manifest:   m,
		commit:     c,
		repo:       r,
	}, nil
}

// ResolveTreePath walks p from the directory view t and returns either a
// *TreeObject for a directory or a *BlobObject for a file.
func (r *LocalHgRepository) ResolveTreePath(t *TreeObject, p string) (HgObject, error) {
	if t == nil || t.manifest == nil {
		return nil, fmt.Errorf("tree view has no manifest")
	}
	cur := t.Path
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		return t, nil
	}
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return nil, ErrObjectNotFound
		}
		next := part
		if cur != "" {
			next = cur + "/" + part
		}
		if item, ok := t.manifest.Get(next); ok {
			// A file: the path must not continue after it.
			if part != parts[len(parts)-1] {
				return nil, ErrObjectNotFound
			}
			return r.readBlob(next, item.Node, item.Flags, item.Mode)
		}
		if t.manifest.HasDir(next) {
			cur = next
			continue
		}
		return nil, ErrObjectNotFound
	}
	return &TreeObject{
		Id:         t.manifest.Id,
		Path:       cur,
		ObjectList: t.manifest.Children(cur),
		manifest:   t.manifest,
		commit:     t.commit,
		repo:       t.repo,
	}, nil
}
