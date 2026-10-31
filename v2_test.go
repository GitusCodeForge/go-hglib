package hglib

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func runHgCfg(t *testing.T, dir string, cfg []string, args ...string) string {
	t.Helper()
	full := append([]string{}, cfg...)
	full = append(full, args...)
	cmd := exec.Command("hg", full...)
	cmd.Dir = dir
	cmd.Env = hgTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hg %v in %s: %s: %s", args, dir, err, out)
	}
	return string(out)
}

func v2Config() []string {
	return []string{
		"--config", "experimental.revlogv2=enable-unstable-format-and-corrupt-my-data",
		"--config", "storage.fileindex.slow-path=allow",
	}
}

func fileIndexConfig() []string {
	return []string{"--config", "storage.fileindex.slow-path=allow"}
}

func newHgV2Repo(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	runHgCfg(t, dir, v2Config(), "init", name)
	return filepath.Join(dir, name)
}

func newHgChangelogV2Repo(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := []string{
		"--config", "format.exp-use-changelog-v2=enable-unstable-format-and-corrupt-my-data",
		"--config", "storage.fileindex.slow-path=allow",
	}
	runHgCfg(t, dir, cfg, "init", name)
	return filepath.Join(dir, name)
}

func hgNodeAtCfg(t *testing.T, repo string, cfg []string, rev string) string {
	t.Helper()
	return strings.TrimSpace(runHgCfg(t, repo, cfg, "log", "-r", rev, "-T", "{node}"))
}

func TestVersion2Repository(t *testing.T) {
	repo := newHgV2Repo(t, "repo")
	cfg := fileIndexConfig()
	run := func(args ...string) string { return runHgCfg(t, repo, cfg, args...) }

	writeFile(t, repo, "a.txt", "hello\n")
	writeFile(t, repo, "src/main.go", "package main\n")
	run("add")
	run("commit", "-m", "initial")

	var b strings.Builder
	for i := 0; i < 600; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("z", i%20))
		b.WriteString("\n")
	}
	writeFile(t, repo, "big.txt", b.String())
	run("add", "big.txt")
	run("commit", "-m", "add big")
	for round := 0; round < 5; round++ {
		lines := strings.Split(b.String(), "\n")
		lines[10+round] = "changed " + string(rune('0'+round))
		b.Reset()
		b.WriteString(strings.Join(lines, "\n"))
		writeFile(t, repo, "big.txt", b.String())
		run("commit", "-m", "change")
	}

	longPath := "src/" + strings.Repeat("long-directory-name/", 10) + "file.txt"
	writeFile(t, repo, longPath, "long\n")
	run("add", longPath)
	run("commit", "-m", "long path")

	run("mv", "a.txt", "renamed.txt")
	run("commit", "-m", "rename")
	run("tag", "v1")
	run("bookmark", "-r", ".", "bm1")
	run("branch", "feature")
	writeFile(t, repo, "feature.txt", "feat\n")
	run("add", "feature.txt")
	run("commit", "-m", "feature")

	r := mustOpen(t, repo)
	if !r.Changelog.V2 || r.Changelog.ChangelogV2 {
		t.Fatalf("changelog: v2=%v changelogv2=%v version=%#x",
			r.Changelog.V2, r.Changelog.ChangelogV2, r.Changelog.FormatVersion)
	}
	if r.Changelog.FormatVersion != revlogV2 {
		t.Fatalf("changelog version = %#x", r.Changelog.FormatVersion)
	}
	if !r.ManifestLog.V2 {
		t.Fatal("manifest is not a version 2 revlog")
	}
	if !r.FileIndexV1 || !r.DotEncode || r.Fncache {
		t.Fatalf("store flags: fileindex=%v dotencode=%v fncache=%v",
			r.FileIndexV1, r.DotEncode, r.Fncache)
	}

	const revisions = 11
	nodes := make([]string, revisions)
	for i := range nodes {
		nodes[i] = hgNodeAtCfg(t, repo, cfg, strconv.Itoa(i))
	}
	tip, err := r.GetTip()
	if err != nil {
		t.Fatal(err)
	}
	if tip.Id != nodes[revisions-1] || tip.Rev != revisions-1 {
		t.Fatalf("tip = %s rev %d, want %s rev %d", tip.Id, tip.Rev, nodes[revisions-1], revisions-1)
	}
	if tip.Branch != "feature" {
		t.Fatalf("tip branch = %s", tip.Branch)
	}

	// Compare changelog fields with hg log.
	for rev := 0; rev < revisions; rev++ {
		c, err := r.GetCommitByRev(rev)
		if err != nil {
			t.Fatalf("rev %d: %s", rev, err)
		}
		wantNode := nodes[rev]
		if c.Id != wantNode {
			t.Fatalf("rev %d node = %s, want %s", rev, c.Id, wantNode)
		}
		wantBranch := strings.TrimSpace(run("log", "-r", strconv.Itoa(rev), "-T", "{branch}"))
		if c.Branch != wantBranch {
			t.Fatalf("rev %d branch = %s, want %s", rev, c.Branch, wantBranch)
		}
		wantDesc := strings.TrimSpace(run("log", "-r", strconv.Itoa(rev), "-T", "{desc|firstline}"))
		if firstLine(c.CommitMessage) != wantDesc {
			t.Fatalf("rev %d desc = %q, want %q", rev, firstLine(c.CommitMessage), wantDesc)
		}
		wantFiles := strings.Fields(run("log", "-r", strconv.Itoa(rev), "-T", "{files}"))
		gotFiles := make(map[string]bool, len(c.Files))
		for _, f := range c.Files {
			gotFiles[f] = true
		}
		for _, f := range wantFiles {
			if !gotFiles[f] {
				t.Fatalf("rev %d files = %v, want %v", rev, c.Files, wantFiles)
			}
		}
	}

	// Version 2 filelogs and manifests must reconstruct deltas correctly.
	// big.txt first appears in revision 1.
	for rev := 1; rev < revisions; rev++ {
		c, err := r.GetCommitByRev(rev)
		if err != nil {
			t.Fatal(err)
		}
		want := run("cat", "-r", strconv.Itoa(rev), "--", "big.txt")
		got, err := r.GetFileContent(c, "big.txt")
		if err != nil {
			t.Fatalf("rev %d big.txt: %s", rev, err)
		}
		if string(got) != want {
			t.Fatalf("rev %d big.txt differs: %d vs %d bytes", rev, len(got), len(want))
		}
	}
	filelog, err := r.OpenFilelog("big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !filelog.V2 || !strings.HasSuffix(filelog.DataPath, ".dat") {
		t.Fatalf("big.txt filelog: v2=%v data=%s", filelog.V2, filelog.DataPath)
	}
	sawFileDelta := false
	for i := range filelog.Entries {
		if filelog.Entries[i].BaseRev != int32(i) {
			sawFileDelta = true
		}
	}
	if !sawFileDelta {
		t.Fatal("big.txt filelog has no delta revision")
	}
	sawManifestDelta := false
	for i := range r.ManifestLog.Entries {
		if r.ManifestLog.Entries[i].BaseRev != int32(i) {
			sawManifestDelta = true
		}
	}
	if !sawManifestDelta {
		t.Fatal("manifest revlog has no delta revision")
	}

	// Manifest comparison against hg manifest --debug.
	for _, rev := range []int{0, 6, revisions - 1} {
		c, err := r.GetCommitByRev(rev)
		if err != nil {
			t.Fatal(err)
		}
		m, err := r.GetManifest(c)
		if err != nil {
			t.Fatalf("rev %d manifest: %s", rev, err)
		}
		want := run("manifest", "--debug", "-r", strconv.Itoa(rev))
		wantMap := make(map[string]string)
		for _, line := range strings.Split(strings.TrimSpace(want), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				wantMap[fields[2]] = fields[0]
			}
		}
		if len(m.ItemList) != len(wantMap) {
			t.Fatalf("rev %d manifest has %d entries, want %d", rev, len(m.ItemList), len(wantMap))
		}
		for _, item := range m.ItemList {
			if wantMap[item.Path] != item.Node {
				t.Fatalf("rev %d manifest %s = %s, want %s", rev, item.Path, item.Node, wantMap[item.Path])
			}
		}
	}

	// Copy metadata is still stored in the filelog data.
	blob, err := r.GetFile(tip, "renamed.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(blob.Data) != "hello\n" {
		t.Fatalf("renamed.txt = %q", blob.Data)
	}
	if blob.CopySource != "a.txt" {
		t.Fatalf("renamed.txt copy source = %q", blob.CopySource)
	}

	// The file index enumerates even the hashed long path.
	tracked, err := r.GetAllTrackedFiles()
	if err != nil {
		t.Fatal(err)
	}
	set := make(map[string]bool)
	for _, p := range tracked {
		set[p] = true
	}
	for _, p := range []string{"big.txt", "src/main.go", "renamed.txt", "feature.txt", longPath} {
		if !set[p] {
			t.Fatalf("tracked files lack %s (got %d paths)", p, len(tracked))
		}
	}
	if !r.HasFilelog(longPath) {
		t.Fatalf("long path %s has no filelog", longPath)
	}

	branches, err := r.GetAllBranchList()
	if err != nil {
		t.Fatal(err)
	}
	if b := branches["default"]; b == nil || b.HeadId != nodes[9] {
		t.Fatalf("default branch = %+v", b)
	}
	if b := branches["feature"]; b == nil || b.HeadId != nodes[10] {
		t.Fatalf("feature branch = %+v", b)
	}
	tags, err := r.GetAllTagList()
	if err != nil {
		t.Fatal(err)
	}
	if tag := tags["v1"]; tag == nil || tag.HeadId != nodes[8] {
		t.Fatalf("tag v1 = %+v", tag)
	}
	bookmarks, err := r.GetAllBookmarkList()
	if err != nil {
		t.Fatal(err)
	}
	if bm := bookmarks["bm1"]; bm == nil || bm.HeadId != nodes[9] {
		t.Fatalf("bookmark bm1 = %+v", bm)
	}

	last, err := r.ResolveLastCommitAtDir(tip, "")
	if err != nil {
		t.Fatal(err)
	}
	wantLast := map[string]string{
		"feature.txt": nodes[10],
		".hgtags":     nodes[9],
		"renamed.txt": nodes[8],
		"big.txt":     nodes[6],
		"src":         nodes[7],
	}
	for name, want := range wantLast {
		if last[name] != want {
			t.Fatalf("last commit for %s = %s, want %s (all: %v)", name, last[name], want, last)
		}
	}

	diff, err := r.GetDiffRev(tip.Rev)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.ItemList) != 1 || diff.ItemList[0].File2 != "b/feature.txt" {
		t.Fatalf("diff = %+v", diff)
	}
	blame, err := r.BlameAtRev(tip.Rev, "renamed.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(blame.LineList) != 1 || len(blame.LineList[0]) != 1 {
		t.Fatalf("blame groups = %+v", blame.LineList)
	}
	if line := blame.LineList[0][0]; line.CommitId != nodes[0] || line.Value != "hello\n" {
		t.Fatalf("blame line = %+v", line)
	}
}

func TestChangelogV2Repository(t *testing.T) {
	repo := newHgChangelogV2Repo(t, "repo")
	cfg := fileIndexConfig()
	run := func(args ...string) string { return runHgCfg(t, repo, cfg, args...) }

	writeFile(t, repo, "a.txt", "one\n")
	run("add")
	run("commit", "-m", "first")
	writeFile(t, repo, "a.txt", "two\n")
	run("commit", "-m", "second")

	r := mustOpen(t, repo)
	if !r.Changelog.V2 || !r.Changelog.ChangelogV2 {
		t.Fatalf("changelog: v2=%v changelogv2=%v version=%#x",
			r.Changelog.V2, r.Changelog.ChangelogV2, r.Changelog.FormatVersion)
	}
	if r.Changelog.FormatVersion != changelogV2 {
		t.Fatalf("changelog version = %#x", r.Changelog.FormatVersion)
	}
	if r.ManifestLog.V2 || r.ManifestLog.FormatVersion != revlogV1 {
		t.Fatalf("manifest version = %#x", r.ManifestLog.FormatVersion)
	}
	if r.FileIndexV1 || !r.Fncache || !r.DotEncode {
		t.Fatalf("store flags: fileindex=%v fncache=%v dotencode=%v",
			r.FileIndexV1, r.Fncache, r.DotEncode)
	}

	nodes := make([]string, 2)
	for i := range nodes {
		nodes[i] = hgNodeAtCfg(t, repo, cfg, strconv.Itoa(i))
	}
	tip, err := r.GetTip()
	if err != nil {
		t.Fatal(err)
	}
	if tip.Id != nodes[1] || tip.P1 != 0 {
		t.Fatalf("tip = %+v", tip)
	}
	if firstLine(tip.CommitMessage) != "second" {
		t.Fatalf("tip message = %q", tip.CommitMessage)
	}
	if len(tip.Files) != 1 || tip.Files[0] != "a.txt" {
		t.Fatalf("tip files = %v", tip.Files)
	}
	if rank := r.Changelog.Entries[tip.Rev].Rank; rank <= 0 {
		t.Fatalf("changelog v2 rank = %d", rank)
	}

	rev0, err := r.GetCommitByRev(0)
	if err != nil {
		t.Fatal(err)
	}
	got0, err := r.GetFileContent(rev0, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got0) != "one\n" {
		t.Fatalf("rev 0 content = %q", got0)
	}
	got1, err := r.GetFileContent(tip, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got1) != "two\n" {
		t.Fatalf("rev 1 content = %q", got1)
	}
	m, err := r.GetManifest(tip)
	if err != nil {
		t.Fatal(err)
	}
	if item, ok := m.Get("a.txt"); !ok || item.Node == "" {
		t.Fatalf("manifest item = %+v, %v", item, ok)
	}
}

func TestVersion2EmptyRepository(t *testing.T) {
	repo := newHgV2Repo(t, "empty")
	r := mustOpen(t, repo)
	if !r.IsEmpty() {
		t.Fatal("new version 2 repository should be empty")
	}
	if _, err := r.GetTip(); err == nil {
		t.Fatal("GetTip should fail on an empty repository")
	}
	branches, err := r.GetAllBranchList()
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 0 {
		t.Fatalf("branches = %v", branches)
	}
	tags, err := r.GetAllTagList()
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 0 {
		t.Fatalf("tags = %v", tags)
	}
	tracked, err := r.GetAllTrackedFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(tracked) != 0 {
		t.Fatalf("tracked = %v", tracked)
	}
}
