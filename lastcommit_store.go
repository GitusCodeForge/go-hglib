package hglib

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"
)

// Directory listings produced by ResolveLastCommitAtDir are also cached on
// disk, inside the repository they belong to:
//
//	<hg dir>/hglib/lastcommit/<changeset>-<directory hash>.json
//
// Keeping the cache there means it is removed with the repository and is not
// copied by hg clone. A changeset node never changes its ancestors or its
// manifest, so an entry can never go stale; an unreadable or mismatched file is
// ignored and recomputed.
const (
	lastCommitStoreVersion       = 1
	lastCommitStoreDirName       = "hglib"
	lastCommitStoreSubDir        = "lastcommit"
	lastCommitStoreCapacity      = 2048
	lastCommitStorePruneInterval = 64
)

type lastCommitStoreFile struct {
	Version   int               `json:"version"`
	Commit    string            `json:"commit"`
	Dir       string            `json:"dir"`
	Timestamp int64             `json:"timestamp"`
	Entries   map[string]string `json:"entries"`
}

var lastCommitStoreWrites atomic.Uint64

func isStoreCommitId(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

func (r *LocalHgRepository) lastCommitStoreDir() string {
	return filepath.Join(r.HgDirectoryPath, lastCommitStoreDirName, lastCommitStoreSubDir)
}

func (r *LocalHgRepository) lastCommitStorePath(commitId, dir string) string {
	h := sha256.Sum256([]byte(dir))
	return filepath.Join(r.lastCommitStoreDir(), commitId+"-"+hex.EncodeToString(h[:8])+".json")
}

func (r *LocalHgRepository) loadLastCommitFromDisk(commitId, dir string) (map[string]string, bool) {
	if !isStoreCommitId(commitId) {
		return nil, false
	}
	b, err := os.ReadFile(r.lastCommitStorePath(commitId, dir))
	if err != nil {
		return nil, false
	}
	var f lastCommitStoreFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, false
	}
	if f.Version != lastCommitStoreVersion || f.Commit != commitId || f.Dir != dir {
		return nil, false
	}
	if f.Entries == nil {
		return nil, false
	}
	return f.Entries, true
}

func (r *LocalHgRepository) saveLastCommitToDisk(commitId, dir string, entries map[string]string) {
	if !isStoreCommitId(commitId) {
		return
	}
	storeDir := r.lastCommitStoreDir()
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		return
	}
	b, err := json.Marshal(lastCommitStoreFile{
		Version:   lastCommitStoreVersion,
		Commit:    commitId,
		Dir:       dir,
		Timestamp: time.Now().Unix(),
		Entries:   entries,
	})
	if err != nil {
		return
	}
	f, err := os.CreateTemp(storeDir, ".tmp-*")
	if err != nil {
		return
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return
	}
	if err := f.Close(); err != nil {
		return
	}
	if err := os.Chmod(tmp, 0644); err != nil {
		return
	}
	if err := os.Rename(tmp, r.lastCommitStorePath(commitId, dir)); err != nil {
		return
	}
	if lastCommitStoreWrites.Add(1)%lastCommitStorePruneInterval == 0 {
		pruneLastCommitStore(storeDir, lastCommitStoreCapacity)
	}
}

// ClearLastCommitStore removes the on-disk listings of this repository and the
// process-wide in-memory listings.
func (r *LocalHgRepository) ClearLastCommitStore() error {
	ClearLastCommitCache()
	return os.RemoveAll(r.lastCommitStoreDir())
}

func pruneLastCommitStore(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type item struct {
		name string
		when time.Time
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{e.Name(), info.ModTime()})
	}
	if len(items) <= keep {
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].when.Before(items[j].when) })
	for _, it := range items[:len(items)-keep] {
		os.Remove(filepath.Join(dir, it.name))
	}
}
