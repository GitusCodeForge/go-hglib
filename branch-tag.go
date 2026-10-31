package hglib

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Branch is a Mercurial named branch. Unlike a Git branch, a Mercurial branch
// can have several heads; HeadId is the tipmost one, and HeadIds lists all of
// them, newest first.
type Branch struct {
	Name     string
	HeadId   string
	HeadIds  []string
	IsClosed bool
}

// Tag is an entry of the .hgtags file. Local tags come from .hg/localtags and
// shadow repository tags of the same name.
type Tag struct {
	Name    string
	HeadId  string
	IsLocal bool
	Rev     int
}

// Bookmark is a Mercurial bookmark, the closest equivalent of a Git branch
// pointer.
type Bookmark struct {
	Name      string
	HeadId    string
	IsCurrent bool
}

// GetAllBranchList computes the heads of every named branch. It first tries
// Mercurial's branch2 cache, which records the heads and whether each closes
// the branch; if the cache is missing or stale it scans the changelog. A
// changeset is a head of its branch when no child belongs to the same branch.
func (r *LocalHgRepository) GetAllBranchList() (map[string]*Branch, error) {
	n := r.Changelog.Len()
	if n == 0 {
		return map[string]*Branch{}, nil
	}
	if cached, ok := r.readBranchCache(); ok {
		return cached, nil
	}
	isHead := make([]bool, n)
	for i := range isHead {
		isHead[i] = true
	}
	branchName := make([]string, n)
	closed := make([]bool, n)
	for rev := 0; rev < n; rev++ {
		c, err := r.GetCommitByRev(rev)
		if err != nil {
			return nil, err
		}
		branchName[rev] = c.Branch
		if v, ok := c.Extra["close"]; ok {
			closed[rev] = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
		}
		for _, p := range []int{c.P1, c.P2} {
			if p < 0 || p >= rev {
				continue
			}
			if branchName[p] == branchName[rev] {
				isHead[p] = false
			}
		}
	}

	headsByBranch := make(map[string][]int)
	for rev := 0; rev < n; rev++ {
		if isHead[rev] {
			headsByBranch[branchName[rev]] = append(headsByBranch[branchName[rev]], rev)
		}
	}
	res := make(map[string]*Branch, len(headsByBranch))
	for name, heads := range headsByBranch {
		sort.Sort(sort.Reverse(sort.IntSlice(heads)))
		ids := make([]string, 0, len(heads))
		for _, rev := range heads {
			ids = append(ids, r.Changelog.Entries[rev].Node)
		}
		b := &Branch{
			Name:     name,
			HeadId:   ids[0],
			HeadIds:  ids,
			IsClosed: closed[heads[0]],
		}
		res[name] = b
	}
	return res, nil
}

// readBranchCache reads Mercurial's branch2 cache. The format is
//
//	<tip node> <tip rev> [<filter hash>]
//	<branch head node> <o|c> <branch name>
//	...
//
// The cache is only accepted when its recorded tip is the current changelog
// tip; otherwise the caller scans the changelog instead.
func (r *LocalHgRepository) readBranchCache() (map[string]*Branch, bool) {
	if r.Changelog == nil || r.Changelog.Len() == 0 {
		return nil, false
	}
	candidates := []string{
		filepath.Join(r.HgDirectoryPath, "cache", "branch2-served"),
		filepath.Join(r.HgDirectoryPath, "cache", "branch2-visible"),
		filepath.Join(r.HgDirectoryPath, "cache", "branch2"),
	}
	var data []byte
	var err error
	for _, p := range candidates {
		data, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return nil, false
	}
	header := strings.Fields(lines[0])
	if len(header) < 2 {
		return nil, false
	}
	tipNode := header[0]
	tipRev, err := strconv.Atoi(header[1])
	if err != nil {
		return nil, false
	}
	if tipRev != r.Changelog.Len()-1 {
		return nil, false
	}
	if !strings.EqualFold(tipNode, r.Changelog.Entries[len(r.Changelog.Entries)-1].Node) {
		return nil, false
	}

	type headInfo struct {
		node   string
		closed bool
		rev    int
	}
	heads := make(map[string][]headInfo)
	for _, line := range lines[1:] {
		fields := strings.SplitN(line, " ", 3)
		if len(fields) < 3 {
			continue
		}
		node := fields[0]
		if !IsValidNode(node) {
			continue
		}
		name := fields[2]
		if name == "" {
			continue
		}
		rev, ok := r.Changelog.RevOfNode(node)
		if !ok {
			// The cache mentions a node the changelog does not have; do not
			// trust it.
			return nil, false
		}
		heads[name] = append(heads[name], headInfo{
			node:   node,
			closed: fields[1] == "c",
			rev:    rev,
		})
	}
	if len(heads) == 0 {
		return nil, false
	}
	res := make(map[string]*Branch, len(heads))
	for name, hs := range heads {
		sort.Slice(hs, func(i, j int) bool { return hs[i].rev > hs[j].rev })
		ids := make([]string, 0, len(hs))
		for _, h := range hs {
			ids = append(ids, h.node)
		}
		res[name] = &Branch{
			Name:     name,
			HeadId:   ids[0],
			HeadIds:  ids,
			IsClosed: hs[0].closed,
		}
	}
	return res, true
}

// SyncAllBranchList refreshes r.BranchIndex.
func (r *LocalHgRepository) SyncAllBranchList() error {
	b, err := r.GetAllBranchList()
	if err != nil {
		return err
	}
	r.BranchIndex = b
	return nil
}

// SyncBranch refreshes one entry of BranchIndex.
func (r *LocalHgRepository) SyncBranch(name string) error {
	all, err := r.GetAllBranchList()
	if err != nil {
		return err
	}
	if r.BranchIndex == nil {
		r.BranchIndex = make(map[string]*Branch)
	}
	if b, ok := all[name]; ok {
		r.BranchIndex[name] = b
	} else {
		delete(r.BranchIndex, name)
	}
	return nil
}

// GetTagsAt reads .hgtags from the manifest of rev and merges local tags.
func (r *LocalHgRepository) GetTagsAt(rev int) (map[string]*Tag, error) {
	res := make(map[string]*Tag)
	if rev >= 0 && rev < r.Changelog.Len() {
		c, err := r.GetCommitByRev(rev)
		if err != nil {
			return nil, err
		}
		if err := r.readTagsFromCommit(c, res); err != nil {
			return nil, err
		}
	}
	if err := r.readLocalTags(res); err != nil {
		return nil, err
	}
	r.fillTagRevisions(res)
	return res, nil
}

// GetAllTagList reads the repository tags as they are at the tip.
func (r *LocalHgRepository) GetAllTagList() (map[string]*Tag, error) {
	res := make(map[string]*Tag)
	if !r.IsEmpty() {
		tip, err := r.GetTip()
		if err != nil {
			return nil, err
		}
		if err := r.readTagsFromCommit(tip, res); err != nil {
			return nil, err
		}
	}
	if err := r.readLocalTags(res); err != nil {
		return nil, err
	}
	r.fillTagRevisions(res)
	return res, nil
}

func (r *LocalHgRepository) readTagsFromCommit(c *CommitObject, res map[string]*Tag) error {
	// A tip does not always contain .hgtags: it may be on a branch that never
	// merged the tag commit. Walk first parents until the file is found.
	cur := c
	for cur != nil {
		m, err := r.GetManifest(cur)
		if err != nil {
			return err
		}
		if item, ok := m.Get(".hgtags"); ok {
			raw, err := r.ReadFilelogRevision(".hgtags", item.Node)
			if err != nil {
				return err
			}
			content, _ := FilterFileMetadata(raw)
			parseTagFile(string(content), false, res)
			return nil
		}
		if cur.P1 < 0 || cur.P1 >= cur.Rev {
			break
		}
		cur, err = r.GetCommitByRev(cur.P1)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *LocalHgRepository) readLocalTags(res map[string]*Tag) error {
	b, err := os.ReadFile(filepath.Join(r.HgDirectoryPath, "localtags"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	parseTagFile(string(b), true, res)
	return nil
}

func parseTagFile(content string, local bool, res map[string]*Tag) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		node, name, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		node = strings.TrimSpace(node)
		name = strings.TrimSpace(name)
		if name == "" || !IsValidNode(node) {
			continue
		}
		if name == "tip" {
			// "tip" is a pseudo-tag supplied by Mercurial, not an entry in
			// .hgtags.
			continue
		}
		if node == nullNodeHex {
			delete(res, name)
			continue
		}
		res[name] = &Tag{
			Name:    name,
			HeadId:  node,
			IsLocal: local,
			Rev:     -1,
		}
	}
}

func (r *LocalHgRepository) fillTagRevisions(tags map[string]*Tag) {
	for _, t := range tags {
		if rev, ok := r.Changelog.RevOfNode(t.HeadId); ok {
			t.Rev = rev
		}
	}
}

// SyncAllTagList refreshes r.TagIndex.
func (r *LocalHgRepository) SyncAllTagList() error {
	t, err := r.GetAllTagList()
	if err != nil {
		return err
	}
	r.TagIndex = t
	return nil
}

// GetAllBookmarkList reads .hg/bookmarks. The current bookmark is taken from
// .hg/bookmarks.current.
func (r *LocalHgRepository) GetAllBookmarkList() (map[string]*Bookmark, error) {
	res := make(map[string]*Bookmark)
	path := filepath.Join(r.HgDirectoryPath, "bookmarks")
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		// Shared repositories keep bookmarks in the store.
		b, err = os.ReadFile(filepath.Join(r.StorePath, "bookmarks"))
		if err != nil {
			if os.IsNotExist(err) {
				return res, nil
			}
			return nil, err
		}
	}
	current := ""
	if cb, err := os.ReadFile(filepath.Join(r.HgDirectoryPath, "bookmarks.current")); err == nil {
		current = strings.TrimSpace(string(cb))
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		node, name, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		node = strings.TrimSpace(node)
		name = strings.TrimSpace(name)
		if name == "" || !IsValidNode(node) {
			continue
		}
		res[name] = &Bookmark{
			Name:      name,
			HeadId:    node,
			IsCurrent: name == current,
		}
	}
	return res, nil
}

// SyncAllBookmarkList refreshes r.BookmarkIndex.
func (r *LocalHgRepository) SyncAllBookmarkList() error {
	b, err := r.GetAllBookmarkList()
	if err != nil {
		return err
	}
	r.BookmarkIndex = b
	return nil
}
