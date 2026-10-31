package hglib

import (
	"fmt"
	"strings"
	"sync"
)

// ResolveLastCommitAtDir returns, for every direct entry of dir at c, the node
// id of the newest changeset that touched it, looking only at the ancestors of
// c. For a file entry that is the newest changeset that changed the path; for
// a directory entry it is the newest changeset that changed anything below it.
//
// The answer is a pure function of (repository, changeset, directory), so it is
// memoized. Merge changesets are skipped, mirroring how git log --name-only
// behaves: the change came in through one of the parents, and that parent is
// part of the same walk.
func (r *LocalHgRepository) ResolveLastCommitAtDir(c *CommitObject, dir string) (map[string]string, error) {
	if c == nil {
		return nil, fmt.Errorf("no changeset given")
	}
	if c.Rev < 0 || c.Rev >= r.Changelog.Len() {
		return nil, fmt.Errorf("changeset %s is not part of this repository", c.Id)
	}
	dir = normalizeManifestPath(dir)
	key := r.RootPath + "\x00" + c.Id + "\x00" + dir
	if v, ok := lastCommitCacheGet(key); ok {
		return copyStringMap(v), nil
	}
	// A previous process may have left the listing on disk.
	if v, ok := r.loadLastCommitFromDisk(c.Id, dir); ok {
		lastCommitCachePut(key, v)
		return copyStringMap(v), nil
	}

	m, err := r.GetManifest(c)
	if err != nil {
		return nil, err
	}
	children := m.Children(dir)
	if len(children) == 0 {
		return map[string]string{}, nil
	}
	want := make(map[string]bool, len(children))
	for _, item := range children {
		want[item.Name] = true
	}

	ancestors := make([]bool, c.Rev+1)
	stack := make([]int, 0, 16)
	stack = append(stack, c.Rev)
	for len(stack) > 0 {
		rev := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if rev < 0 || rev >= len(ancestors) || ancestors[rev] {
			continue
		}
		ancestors[rev] = true
		cc, err := r.GetCommitByRev(rev)
		if err != nil {
			return nil, err
		}
		if cc.P1 >= 0 {
			stack = append(stack, cc.P1)
		}
		if cc.P2 >= 0 {
			stack = append(stack, cc.P2)
		}
	}

	res := make(map[string]string, len(children))
	for rev := c.Rev; rev >= 0 && len(res) < len(children); rev-- {
		if !ancestors[rev] {
			continue
		}
		cc, err := r.GetCommitByRev(rev)
		if err != nil {
			return nil, err
		}
		if cc.P2 >= 0 {
			continue
		}
		for _, file := range cc.Files {
			name := lastCommitDirectChild(file, dir)
			if name == "" || !want[name] {
				continue
			}
			if _, ok := res[name]; !ok {
				res[name] = cc.Id
			}
		}
	}
	r.saveLastCommitToDisk(c.Id, dir, res)
	lastCommitCachePut(key, res)
	return copyStringMap(res), nil
}

func lastCommitDirectChild(p, dir string) string {
	p = strings.TrimPrefix(p, "/")
	if dir != "" {
		if !strings.HasPrefix(p, dir+"/") {
			return ""
		}
		p = p[len(dir)+1:]
	}
	if p == "" {
		return ""
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		p = p[:i]
	}
	return p
}

func copyStringMap(m map[string]string) map[string]string {
	res := make(map[string]string, len(m))
	for k, v := range m {
		res[k] = v
	}
	return res
}

const lastCommitCacheCapacity = 512

var lastCommitCache = struct {
	mu    sync.Mutex
	items map[string]map[string]string
	order []string
}{
	items: make(map[string]map[string]string),
}

func lastCommitCacheGet(key string) (map[string]string, bool) {
	lastCommitCache.mu.Lock()
	defer lastCommitCache.mu.Unlock()
	v, ok := lastCommitCache.items[key]
	return v, ok
}

func lastCommitCachePut(key string, value map[string]string) {
	lastCommitCache.mu.Lock()
	defer lastCommitCache.mu.Unlock()
	if _, ok := lastCommitCache.items[key]; ok {
		lastCommitCache.items[key] = value
		return
	}
	lastCommitCache.items[key] = value
	lastCommitCache.order = append(lastCommitCache.order, key)
	for len(lastCommitCache.order) > lastCommitCacheCapacity {
		old := lastCommitCache.order[0]
		lastCommitCache.order = lastCommitCache.order[1:]
		delete(lastCommitCache.items, old)
	}
}

// ClearLastCommitCache drops every memoized directory listing.
func ClearLastCommitCache() {
	lastCommitCache.mu.Lock()
	defer lastCommitCache.mu.Unlock()
	lastCommitCache.items = make(map[string]map[string]string)
	lastCommitCache.order = nil
}
