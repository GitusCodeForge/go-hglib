package hglib

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

var (
	ErrNotHgRepository = errors.New("not a mercurial repository")
	ErrEmptyRepository = errors.New("the mercurial repository has no changesets")
)

// LocalHgRepository is a handle on a Mercurial repository on disk. It parses
// the store directly; it does not need the hg executable to read history.
type LocalHgRepository struct {
	// RootPath is the working directory (the parent of .hg).
	RootPath string
	// HgDirectoryPath is the .hg directory.
	HgDirectoryPath string
	// StorePath is .hg/store for a modern repository, or .hg for a repository
	// that predates the "store" requirement.
	StorePath string
	// SharedPath is set when .hg/sharedpath points at another repository.
	SharedPath string

	Requirements      []string
	StoreRequirements []string
	DotEncode         bool
	Fncache           bool

	Config      *HGRC
	Description string
	DefaultUser string

	Changelog   *Revlog
	ManifestLog *Revlog

	BranchIndex   map[string]*Branch
	TagIndex      map[string]*Tag
	BookmarkIndex map[string]*Bookmark

	filelogs   map[string]*Revlog
	filelogsMu sync.Mutex
}

// IsValidHgRepository reports whether p looks like a Mercurial repository or
// like a .hg directory.
func IsValidHgRepository(p string) bool {
	_, hg, ok := locateHgDirectory(p)
	if !ok {
		return false
	}
	if _, err := os.Stat(filepath.Join(hg, "requires")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(hg, "store")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(hg, "00changelog.i")); err == nil {
		return true
	}
	return false
}

func locateHgDirectory(p string) (root, hg string, ok bool) {
	if p == "" {
		return "", "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if st, err := os.Stat(filepath.Join(abs, ".hg")); err == nil && st.IsDir() {
		return abs, filepath.Join(abs, ".hg"), true
	}
	if filepath.Base(abs) == ".hg" {
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			return filepath.Dir(abs), abs, true
		}
	}
	// A store directory passed directly: .../.hg/store.
	if filepath.Base(abs) == "store" && filepath.Base(filepath.Dir(abs)) == ".hg" {
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			return filepath.Dir(filepath.Dir(abs)), filepath.Dir(abs), true
		}
	}
	// A bare store directory passed directly.
	if _, err := os.Stat(filepath.Join(abs, "requires")); err == nil {
		return filepath.Dir(abs), abs, true
	}
	if _, err := os.Stat(filepath.Join(abs, "store", "requires")); err == nil {
		return filepath.Dir(abs), abs, true
	}
	if _, err := os.Stat(filepath.Join(abs, "00changelog.i")); err == nil {
		return filepath.Dir(abs), abs, true
	}
	return "", "", false
}

// NewLocalHgRepository opens a repository. p may be the working directory or
// the .hg directory itself.
func NewLocalHgRepository(p string) (*LocalHgRepository, error) {
	root, hg, ok := locateHgDirectory(p)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotHgRepository, p)
	}
	r := &LocalHgRepository{
		RootPath:        root,
		HgDirectoryPath: hg,
	}
	var err error
	r.Requirements, err = readRequires(filepath.Join(hg, "requires"))
	if err != nil {
		return nil, err
	}
	r.StoreRequirements, err = readRequires(filepath.Join(hg, "store", "requires"))
	if err != nil {
		return nil, err
	}
	if len(r.StoreRequirements) == 0 {
		r.StoreRequirements = r.Requirements
	}
	r.DotEncode = hasRequirement(r.StoreRequirements, "dotencode")
	r.Fncache = hasRequirement(r.StoreRequirements, "fncache")

	shared, err := readSharedPath(hg)
	if err != nil {
		return nil, err
	}
	r.SharedPath = shared
	if shared != "" {
		r.StorePath = findStoreDir(shared)
	} else {
		r.StorePath = findStoreDir(hg)
	}

	changelogPath := localPath(r.StorePath, "00changelog.i")
	r.Changelog, err = OpenRevlogOrEmpty(changelogPath)
	if err != nil {
		return nil, err
	}
	manifestPath := localPath(r.StorePath, "00manifest.i")
	r.ManifestLog, err = OpenRevlogOrEmpty(manifestPath)
	if err != nil {
		r.Changelog.Close()
		return nil, err
	}

	r.Config, err = ReadHGRC(filepath.Join(hg, "hgrc"))
	if err != nil {
		r.Changelog.Close()
		r.ManifestLog.Close()
		return nil, err
	}
	r.Description = r.Config.GetDefault("web", "description", "")
	if r.Description == "" {
		if b, readErr := os.ReadFile(filepath.Join(hg, "description")); readErr == nil {
			r.Description = string(b)
		}
	}
	r.DefaultUser = r.Config.GetDefault("ui", "username", "")
	return r, nil
}

// InitLocalHgRepository creates a new Mercurial repository at p by running
// hg init. Mercurial has no bare-repository mode; the repository has a working
// directory, which is simply left empty.
func InitLocalHgRepository(p string) (*LocalHgRepository, error) {
	if err := os.MkdirAll(p, 0755); err != nil {
		return nil, err
	}
	cmd := exec.Command("hg", "init", p)
	cmd.Env = hgEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("hg init: %s: %s", err, strings.TrimSpace(string(out)))
	}
	return NewLocalHgRepository(p)
}

func hgEnv() []string {
	env := os.Environ()
	env = append(env,
		"HGPLAIN=1",
		"HGENCODING=UTF-8",
		"HGPLAINEXCEPT=",
	)
	return env
}

func readSharedPath(hg string) (string, error) {
	b, err := os.ReadFile(filepath.Join(hg, "sharedpath"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	p := strings.TrimSpace(string(b))
	if p == "" {
		return "", nil
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(hg, p)
	}
	return filepath.Clean(p), nil
}

func findStoreDir(hg string) string {
	reqs, _ := readRequires(filepath.Join(hg, "requires"))
	storeReqs, _ := readRequires(filepath.Join(hg, "store", "requires"))
	if hasRequirement(reqs, "store") || hasRequirement(storeReqs, "store") {
		return filepath.Join(hg, "store")
	}
	// Repositories that use share-safe but have no changesets yet still have a
	// store/requires file, which is enough to know the store is in use.
	if _, err := os.Stat(filepath.Join(hg, "store", "requires")); err == nil {
		return filepath.Join(hg, "store")
	}
	if st, err := os.Stat(filepath.Join(hg, "store")); err == nil && st.IsDir() {
		return filepath.Join(hg, "store")
	}
	return hg
}

// Close releases open revlog files.
func (r *LocalHgRepository) Close() error {
	var firstErr error
	r.filelogsMu.Lock()
	for _, rl := range r.filelogs {
		if err := rl.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	r.filelogs = nil
	r.filelogsMu.Unlock()
	if r.Changelog != nil {
		if err := r.Changelog.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if r.ManifestLog != nil {
		if err := r.ManifestLog.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Refresh reopens the changelog and manifest and forgets cached filelogs, so
// that a long-lived handle sees commits that were pushed after it was opened.
// Revision numbers that existed before the refresh keep their meaning.
func (r *LocalHgRepository) Refresh() error {
	r.filelogsMu.Lock()
	for _, rl := range r.filelogs {
		rl.Close()
	}
	r.filelogs = nil
	r.filelogsMu.Unlock()
	if r.Changelog != nil {
		r.Changelog.Close()
	}
	if r.ManifestLog != nil {
		r.ManifestLog.Close()
	}
	var err error
	r.Changelog, err = OpenRevlogOrEmpty(localPath(r.StorePath, "00changelog.i"))
	if err != nil {
		return err
	}
	r.ManifestLog, err = OpenRevlogOrEmpty(localPath(r.StorePath, "00manifest.i"))
	return err
}

// IsEmpty reports whether the repository has no changesets.
func (r *LocalHgRepository) IsEmpty() bool {
	return r.Changelog == nil || r.Changelog.Len() == 0
}

// TipRev returns the revision number of the tip, which is the highest
// revision number in the changelog.
func (r *LocalHgRepository) TipRev() (int, error) {
	if r.IsEmpty() {
		return 0, ErrEmptyRepository
	}
	return r.Changelog.Len() - 1, nil
}

// GetTip returns the tip changeset.
func (r *LocalHgRepository) GetTip() (*CommitObject, error) {
	rev, err := r.TipRev()
	if err != nil {
		return nil, err
	}
	return r.GetCommitByRev(rev)
}

// GetCommitByRev parses one changelog revision.
func (r *LocalHgRepository) GetCommitByRev(rev int) (*CommitObject, error) {
	if rev < 0 || rev >= r.Changelog.Len() {
		return nil, ErrRevNotFound
	}
	data, err := r.Changelog.RawRevision(rev)
	if err != nil {
		return nil, err
	}
	c, err := parseCommitObject(r.Changelog.Entries[rev].Node, rev, data)
	if err != nil {
		return nil, err
	}
	c.P1 = int(r.Changelog.Entries[rev].P1)
	c.P2 = int(r.Changelog.Entries[rev].P2)
	if c.P1 >= 0 {
		c.ParentIdList = append(c.ParentIdList, r.Changelog.Entries[c.P1].Node)
	}
	if c.P2 >= 0 {
		c.ParentIdList = append(c.ParentIdList, r.Changelog.Entries[c.P2].Node)
	}
	return c, nil
}

// GetCommit resolves a revision number, a full node id, a unique node prefix,
// or "tip".
func (r *LocalHgRepository) GetCommit(revOrNode string) (*CommitObject, error) {
	s := strings.TrimSpace(revOrNode)
	if s == "" || s == "null" {
		return nil, ErrRevNotFound
	}
	if s == "tip" {
		return r.GetTip()
	}
	if s == "." {
		p1, p2, err := r.GetWorkingDirParents()
		if err != nil {
			return nil, err
		}
		if p1 == nullNodeHex {
			if p2 != nullNodeHex {
				return r.GetCommitByNode(p2)
			}
			return nil, ErrEmptyRepository
		}
		return r.GetCommitByNode(p1)
	}
	if n, err := strconv.Atoi(s); err == nil {
		return r.GetCommitByRev(n)
	}
	if IsValidNode(s) {
		rev, ok := r.Changelog.RevOfNode(s)
		if !ok {
			return nil, ErrRevNotFound
		}
		return r.GetCommitByRev(rev)
	}
	if IsValidNodePrefix(s) {
		rev, err := r.Changelog.RevOfPrefix(s)
		if err != nil {
			return nil, err
		}
		return r.GetCommitByRev(rev)
	}
	return nil, ErrRevNotFound
}

// GetCommitByNode resolves a full node id without accepting a prefix.
func (r *LocalHgRepository) GetCommitByNode(node string) (*CommitObject, error) {
	rev, ok := r.Changelog.RevOfNode(node)
	if !ok {
		return nil, ErrRevNotFound
	}
	return r.GetCommitByRev(rev)
}

// GetManifestAtRev returns the manifest of a changelog revision.
func (r *LocalHgRepository) GetManifestAtRev(rev int) (*ManifestObject, error) {
	c, err := r.GetCommitByRev(rev)
	if err != nil {
		return nil, err
	}
	return r.GetManifest(c)
}

// GetTreeAtRev returns a directory listing at a changelog revision.
func (r *LocalHgRepository) GetTreeAtRev(rev int, dir string) (*TreeObject, error) {
	c, err := r.GetCommitByRev(rev)
	if err != nil {
		return nil, err
	}
	return r.GetTree(c, dir)
}

// GetFileAtRev returns a file at a changelog revision.
func (r *LocalHgRepository) GetFileAtRev(rev int, filePath string) (*BlobObject, error) {
	c, err := r.GetCommitByRev(rev)
	if err != nil {
		return nil, err
	}
	return r.GetFile(c, filePath)
}

// GetAllChangesets parses the whole changelog, oldest first.
func (r *LocalHgRepository) GetAllChangesets() ([]*CommitObject, error) {
	n := r.Changelog.Len()
	res := make([]*CommitObject, 0, n)
	for i := 0; i < n; i++ {
		c, err := r.GetCommitByRev(i)
		if err != nil {
			return nil, err
		}
		res = append(res, c)
	}
	return res, nil
}

// GetCommitHistory walks first parents from cid. The result starts with the
// commit itself, like gitlib's GetCommitHistory.
func (r *LocalHgRepository) GetCommitHistory(cid string) ([]CommitObject, error) {
	return r.GetCommitHistoryN(cid, 0)
}

// GetCommitHistoryN walks at most n first parents; n <= 0 means no limit.
func (r *LocalHgRepository) GetCommitHistoryN(cid string, n int) ([]CommitObject, error) {
	cur, err := r.GetCommit(cid)
	if err != nil {
		return nil, err
	}
	limitless := n <= 0
	res := make([]CommitObject, 0, 16)
	for cur != nil {
		res = append(res, *cur)
		if !limitless {
			n--
			if n <= 0 {
				break
			}
		}
		if cur.P1 < 0 || cur.P1 >= cur.Rev {
			break
		}
		cur, err = r.GetCommitByRev(cur.P1)
		if err != nil {
			return nil, err
		}
	}
	return res, nil
}

// GetBranchCommitHistory returns the full history reachable through first
// parents from the branch head.
func (r *LocalHgRepository) GetBranchCommitHistory(b Branch) ([]CommitObject, error) {
	return r.GetCommitHistory(b.HeadId)
}

// GetBranchCommitHistoryN is GetBranchCommitHistory with a limit.
func (r *LocalHgRepository) GetBranchCommitHistoryN(b Branch, n int) ([]CommitObject, error) {
	return r.GetCommitHistoryN(b.HeadId, n)
}

// GetWorkingDirBranch returns the branch recorded in .hg/branch, or "default"
// when the working directory is not on a branch.
func (r *LocalHgRepository) GetWorkingDirBranch() string {
	b, err := os.ReadFile(filepath.Join(r.HgDirectoryPath, "branch"))
	if err != nil {
		return "default"
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "default"
	}
	return s
}

// GetChangeContext is a convenience wrapper returning the manifest and the
// direct entries of dir for a changeset.
func (r *LocalHgRepository) GetChangeContext(c *CommitObject, dir string) (*ManifestObject, []TreeObjectItem, error) {
	m, err := r.GetManifest(c)
	if err != nil {
		return nil, nil, err
	}
	dir = normalizeManifestPath(dir)
	if dir != "" && !m.HasDir(dir) {
		return nil, nil, ErrObjectNotFound
	}
	return m, m.Children(dir), nil
}
