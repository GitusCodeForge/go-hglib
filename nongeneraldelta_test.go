package hglib

import (
	"path/filepath"
	"strings"
	"testing"
)

// A non-generaldelta revlog stores the chain base in the index entry, and every
// delta is against the previous revision. Reading a chain of length three or
// more therefore cannot follow the base link directly.
func TestNonGeneralDelta(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	runHg(t, dir, "init", "--config", "format.usegeneraldelta=false", "repo")

	var b strings.Builder
	for i := 0; i < 600; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("y", i%20))
		b.WriteString("\n")
	}
	writeFile(t, repo, "big.txt", b.String())
	runHg(t, repo, "add")
	runHg(t, repo, "commit", "-m", "add big file")
	for round := 0; round < 6; round++ {
		lines := strings.Split(b.String(), "\n")
		lines[10+round] = "changed " + string(rune('0'+round))
		b.Reset()
		b.WriteString(strings.Join(lines, "\n"))
		writeFile(t, repo, "big.txt", b.String())
		runHg(t, repo, "commit", "-m", "change")
	}

	r := mustOpen(t, repo)
	if r.Changelog.GeneralDelta || r.ManifestLog.GeneralDelta {
		t.Fatal("expected a non-generaldelta repository")
	}
	for rev := 0; rev < 7; rev++ {
		c, err := r.GetCommitByRev(rev)
		if err != nil {
			t.Fatalf("rev %d: %s", rev, err)
		}
		want := runHg(t, repo, "cat", "-r", string(rune('0'+rev)), "--", "big.txt")
		got, err := r.GetFileContent(c, "big.txt")
		if err != nil {
			t.Fatalf("rev %d: %s", rev, err)
		}
		if string(got) != want {
			t.Fatalf("rev %d content differs: %d vs %d bytes", rev, len(got), len(want))
		}
		wantManifest := runHg(t, repo, "manifest", "--debug", "-r", string(rune('0'+rev)))
		wantCount := len(strings.Split(strings.TrimSpace(wantManifest), "\n"))
		m, err := r.GetManifest(c)
		if err != nil {
			t.Fatalf("rev %d manifest: %s", rev, err)
		}
		if len(m.ItemList) != wantCount {
			t.Fatalf("rev %d manifest has %d entries, want %d", rev, len(m.ItemList), wantCount)
		}
	}
}
