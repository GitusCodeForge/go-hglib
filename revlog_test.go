package hglib

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenHeaderOnlyRevlog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.i")
	if err := os.WriteFile(path, []byte{0, 0, 0, 1}, 0644); err != nil {
		t.Fatal(err)
	}
	r, err := OpenRevlog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Len() != 0 {
		t.Fatalf("len = %d", r.Len())
	}
	if _, err := r.RawRevision(0); err == nil {
		t.Fatal("RawRevision on an empty revlog should fail")
	}
}

func TestRevisionHashing(t *testing.T) {
	repo := newHgRepo(t, "repo")
	writeFile(t, repo, "a.txt", "one\n")
	runHg(t, repo, "add")
	runHg(t, repo, "commit", "-m", "one")
	writeFile(t, repo, "a.txt", "two\n")
	runHg(t, repo, "commit", "-m", "two")

	r := mustOpen(t, repo)
	n0 := r.Changelog.Entries[0].Node
	n1 := r.Changelog.Entries[1].Node
	raw0, err := r.Changelog.RawRevision(0)
	if err != nil {
		t.Fatal(err)
	}
	if got := HashRevision(raw0, nullNodeHex, nullNodeHex); got != n0 {
		t.Fatalf("changelog rev 0 hash = %s, want %s", got, n0)
	}
	raw1, err := r.Changelog.RawRevision(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := HashRevision(raw1, n0, nullNodeHex); got != n1 {
		t.Fatalf("changelog rev 1 hash = %s, want %s", got, n1)
	}

	fl, err := r.OpenFilelog("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	for rev := 0; rev < fl.Len(); rev++ {
		raw, err := fl.RawRevision(rev)
		if err != nil {
			t.Fatal(err)
		}
		p1, p2 := fl.ParentNodes(rev)
		if got := HashRevision(raw, p1, p2); got != fl.Entries[rev].Node {
			t.Fatalf("filelog rev %d hash = %s, want %s", rev, got, fl.Entries[rev].Node)
		}
	}
}
