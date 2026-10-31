package hglib

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFetchRemote(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	runHg(t, dir, "init", "src")
	writeFile(t, src, "a.txt", "one\n")
	runHg(t, src, "add")
	runHg(t, src, "commit", "-m", "one")

	dst := filepath.Join(dir, "dst")
	runHg(t, dir, "clone", src, dst)

	writeFile(t, src, "b.txt", "two\n")
	runHg(t, src, "add", "b.txt")
	runHg(t, src, "commit", "-m", "two")
	srcNode := hgNodeAt(t, src, "tip")

	// Add a named path to the destination's hgrc.
	r := mustOpen(t, dst)
	if err := SetConfigValue(filepath.Join(dst, ".hg", "hgrc"), "paths", "origin", src); err != nil {
		t.Fatal(err)
	}
	if err := r.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
	remotes, err := r.GetAllRemote()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range remotes {
		if name == "origin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("remotes = %v, want origin", remotes)
	}
	if ok, err := r.HasRemote("origin"); err != nil || !ok {
		t.Fatalf("HasRemote = %v, %v", ok, err)
	}
	if err := r.FetchRemote("origin"); err != nil {
		t.Fatal(err)
	}
	tip, err := r.GetTip()
	if err != nil {
		t.Fatal(err)
	}
	if tip.Id != srcNode {
		t.Fatalf("tip after pull = %s, want %s", tip.Id, srcNode)
	}
	_ = os.Remove(filepath.Join(dst, ".hg", "hgrc"))
}
