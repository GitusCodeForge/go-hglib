package hglib

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func hgTestEnv() []string {
	return append(os.Environ(),
		"HGPLAIN=1",
		"HGENCODING=UTF-8",
		"HGUSER=Test User <test@example.com>",
	)
}

func runHg(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("hg", args...)
	cmd.Dir = dir
	cmd.Env = hgTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hg %v in %s: %s: %s", args, dir, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, repo, name, content string) {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func newHgRepo(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, name)
	runHg(t, dir, "init", name)
	return repo
}

func mustOpen(t *testing.T, repo string) *LocalHgRepository {
	t.Helper()
	r, err := NewLocalHgRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func hgNodeAt(t *testing.T, repo string, rev string) string {
	t.Helper()
	return strings.TrimSpace(runHg(t, repo, "log", "-r", rev, "-T", "{node}"))
}

func hgManifestLine(t *testing.T, repo, rev string) string {
	t.Helper()
	return strings.TrimSpace(runHg(t, repo, "log", "-r", rev, "-T", `{manifest % "{node}"}`))
}

func TestRepositoryBasics(t *testing.T) {
	repo := newHgRepo(t, "repo")
	writeFile(t, repo, "a.txt", "hello\n")
	writeFile(t, repo, "src/main.go", "package main\n")
	writeFile(t, repo, "src/sub/x.txt", "nested\n")
	runHg(t, repo, "add")
	runHg(t, repo, "commit", "-m", "initial commit")
	writeFile(t, repo, "a.txt", "hello world\n")
	runHg(t, repo, "commit", "-m", "second")
	runHg(t, repo, "tag", "v1")
	runHg(t, repo, "bookmark", "-r", ".", "bm1")
	runHg(t, repo, "branch", "feature")
	writeFile(t, repo, "f.txt", "feat\n")
	runHg(t, repo, "add", "f.txt")
	runHg(t, repo, "commit", "-m", "feature commit")

	r := mustOpen(t, repo)
	if r.IsEmpty() {
		t.Fatal("repository should not be empty")
	}
	// The working directory, the .hg directory and .hg/store all identify the
	// same repository.
	for _, p := range []string{repo, filepath.Join(repo, ".hg"), filepath.Join(repo, ".hg", "store")} {
		rr, err := NewLocalHgRepository(p)
		if err != nil {
			t.Fatalf("open %s: %s", p, err)
		}
		tip, err := rr.GetTip()
		rr.Close()
		if err != nil {
			t.Fatalf("tip of %s: %s", p, err)
		}
		if tip.CommitMessage != "feature commit" {
			t.Fatalf("tip of %s = %q", p, tip.CommitMessage)
		}
	}
	nodes := make([]string, 4)
	for i := range nodes {
		nodes[i] = hgNodeAt(t, repo, string(rune('0'+i)))
	}
	tip, err := r.GetTip()
	if err != nil {
		t.Fatal(err)
	}
	if tip.Id != nodes[3] {
		t.Fatalf("tip = %s, want %s", tip.Id, nodes[3])
	}
	if got := hgManifestLine(t, repo, "3"); tip.TreeObjId != got {
		t.Fatalf("tip manifest = %s, want %s", tip.TreeObjId, got)
	}

	branches := []string{"default", "default", "default", "feature"}
	messages := []string{"initial commit", "second", "Added tag v1 for changeset " + nodes[1][:12], "feature commit"}
	files := [][]string{
		{"a.txt", "src/main.go", "src/sub/x.txt"},
		{"a.txt"},
		{".hgtags"},
		{"f.txt"},
	}
	for i := 0; i < 4; i++ {
		c, err := r.GetCommitByRev(i)
		if err != nil {
			t.Fatalf("rev %d: %s", i, err)
		}
		if c.Id != nodes[i] {
			t.Fatalf("rev %d node = %s, want %s", i, c.Id, nodes[i])
		}
		if c.Branch != branches[i] {
			t.Fatalf("rev %d branch = %s, want %s", i, c.Branch, branches[i])
		}
		if !strings.HasPrefix(c.CommitMessage, messages[i]) {
			t.Fatalf("rev %d message = %q, want prefix %q", i, c.CommitMessage, messages[i])
		}
		if c.AuthorInfo.AuthorName != "Test User" || c.AuthorInfo.AuthorEmail != "test@example.com" {
			t.Fatalf("rev %d author = %+v", i, c.AuthorInfo)
		}
		if len(c.Files) != len(files[i]) {
			t.Fatalf("rev %d files = %v, want %v", i, c.Files, files[i])
		}
		for j := range files[i] {
			if c.Files[j] != files[i][j] {
				t.Fatalf("rev %d files = %v, want %v", i, c.Files, files[i])
			}
		}
		wantP1 := i - 1
		if c.P1 != wantP1 {
			t.Fatalf("rev %d p1 = %d, want %d", i, c.P1, wantP1)
		}
		if c.P2 != -1 {
			t.Fatalf("rev %d p2 = %d, want -1", i, c.P2)
		}
	}

	// Resolve by revision, by full node, by prefix and by "tip".
	for _, spec := range []string{"3", nodes[3], nodes[3][:8], "tip"} {
		c, err := r.GetCommit(spec)
		if err != nil {
			t.Fatalf("GetCommit(%q): %s", spec, err)
		}
		if c.Id != nodes[3] {
			t.Fatalf("GetCommit(%q) = %s", spec, c.Id)
		}
	}

	hist, err := r.GetCommitHistoryN("tip", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].Id != nodes[3] || hist[1].Id != nodes[2] {
		t.Fatalf("history = %+v", hist)
	}

	// Manifest comparison against hg manifest --debug.
	manifestOut := runHg(t, repo, "manifest", "--debug", "-r", "3")
	wantManifest := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(manifestOut), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		wantManifest[fields[2]] = fields[0]
	}
	m, err := r.GetManifest(tip)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ItemList) != len(wantManifest) {
		t.Fatalf("manifest has %d entries, hg has %d", len(m.ItemList), len(wantManifest))
	}
	for _, item := range m.ItemList {
		if wantManifest[item.Path] != item.Node {
			t.Fatalf("manifest %s = %s, want %s", item.Path, item.Node, wantManifest[item.Path])
		}
	}

	tree, err := r.GetTree(tip, "")
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]TreeObjectItem)
	for _, item := range tree.ObjectList {
		names[item.Name] = item
	}
	for _, name := range []string{".hgtags", "a.txt", "f.txt", "src"} {
		if _, ok := names[name]; !ok {
			t.Fatalf("root tree lacks %s: %+v", name, tree.ObjectList)
		}
	}
	if !names["src"].IsDir {
		t.Fatalf("src should be a directory: %+v", names["src"])
	}
	if names["a.txt"].IsDir || names["a.txt"].Hash != wantManifest["a.txt"] {
		t.Fatalf("a.txt entry = %+v", names["a.txt"])
	}

	srcTree, err := r.GetTree(tip, "src")
	if err != nil {
		t.Fatal(err)
	}
	srcNames := make(map[string]bool)
	for _, item := range srcTree.ObjectList {
		srcNames[item.Name] = item.IsDir
	}
	if _, ok := srcNames["main.go"]; !ok {
		t.Fatalf("src tree lacks main.go: %+v", srcTree.ObjectList)
	}
	if isDir, ok := srcNames["sub"]; !ok || !isDir {
		t.Fatalf("src tree sub = %v, %v", isDir, ok)
	}

	obj, err := r.ResolveTreePath(tree, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	blob, ok := obj.(*BlobObject)
	if !ok {
		t.Fatalf("src/main.go resolved to %T", obj)
	}
	if string(blob.Data) != "package main\n" {
		t.Fatalf("src/main.go = %q", blob.Data)
	}
	obj, err = r.ResolveTreePath(tree, "src")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := obj.(*TreeObject); !ok {
		t.Fatalf("src resolved to %T", obj)
	}

	c0, err := r.GetCommitByRev(0)
	if err != nil {
		t.Fatal(err)
	}
	content, err := r.GetFileContent(c0, "src/sub/x.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "nested\n" {
		t.Fatalf("nested content = %q", content)
	}

	// Branches, tags and bookmarks.
	branchesMap, err := r.GetAllBranchList()
	if err != nil {
		t.Fatal(err)
	}
	if b := branchesMap["default"]; b == nil || b.HeadId != nodes[2] {
		t.Fatalf("default branch = %+v", b)
	}
	if b := branchesMap["feature"]; b == nil || b.HeadId != nodes[3] {
		t.Fatalf("feature branch = %+v", b)
	}

	tags, err := r.GetAllTagList()
	if err != nil {
		t.Fatal(err)
	}
	if tag := tags["v1"]; tag == nil || tag.HeadId != nodes[1] || tag.Rev != 1 {
		t.Fatalf("tag v1 = %+v", tag)
	}

	bookmarks, err := r.GetAllBookmarkList()
	if err != nil {
		t.Fatal(err)
	}
	if bm := bookmarks["bm1"]; bm == nil || bm.HeadId != nodes[2] {
		t.Fatalf("bookmark bm1 = %+v", bm)
	}

	// Last commit at each direct entry of the root.
	last, err := r.ResolveLastCommitAtDir(tip, "")
	if err != nil {
		t.Fatal(err)
	}
	wantLast := map[string]string{
		".hgtags": nodes[2],
		"a.txt":   nodes[1],
		"f.txt":   nodes[3],
		"src":     nodes[0],
	}
	for name, want := range wantLast {
		if last[name] != want {
			t.Fatalf("last commit for %s = %s, want %s (all: %v)", name, last[name], want, last)
		}
	}
	if _, err := os.Stat(r.lastCommitStorePath(tip.Id, "")); err != nil {
		t.Fatalf("last-commit listing was not written to disk: %s", err)
	}
	ClearLastCommitCache()
	r2, err := NewLocalHgRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	tip2, err := r2.GetCommit(tip.Id)
	if err != nil {
		t.Fatal(err)
	}
	last2, err := r2.ResolveLastCommitAtDir(tip2, "")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range wantLast {
		if last2[name] != want {
			t.Fatalf("cached last commit for %s = %s, want %s", name, last2[name], want)
		}
	}

	// Diff of the feature commit.
	diff, err := r.GetDiffRev(3)
	if err != nil {
		t.Fatal(err)
	}
	if diff.CommitHash != nodes[3] || len(diff.ItemList) != 1 {
		t.Fatalf("diff = %+v", diff)
	}
	item := diff.ItemList[0]
	if item.File2 != "b/f.txt" {
		t.Fatalf("diff file2 = %q", item.File2)
	}
	if len(item.PatchList) != 1 || len(item.PatchList[0].LineList) != 1 {
		t.Fatalf("diff patches = %+v", item.PatchList)
	}
	line := item.PatchList[0].LineList[0]
	if line.Type != APPEND || line.Line != "feat\n" || line.F2LineNum != 1 {
		t.Fatalf("diff line = %+v", line)
	}

	// Annotation.
	blame, err := r.BlameAtRev(1, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(blame.LineList) != 1 || len(blame.LineList[0]) != 1 {
		t.Fatalf("blame groups = %+v", blame.LineList)
	}
	bl := blame.LineList[0][0]
	if bl.CommitId != nodes[1] || bl.Value != "hello world\n" || bl.FinalLineNumber != 1 {
		t.Fatalf("blame line = %+v", bl)
	}
	if info := blame.CommitInfo[nodes[1]]; info == nil || info.Summary != "second" {
		t.Fatalf("blame commit info = %+v", info)
	}

	// "." is the working directory parent, not the tip.
	dot, err := r.GetCommit(".")
	if err != nil {
		t.Fatal(err)
	}
	if dot.Id != nodes[3] {
		t.Fatalf("'.' = %s, want %s", dot.Id, nodes[3])
	}

	// Working directory state.
	p1, p2, err := r.GetWorkingDirParents()
	if err != nil {
		t.Fatal(err)
	}
	if p1 != nodes[3] || p2 != nullNodeHex {
		t.Fatalf("dirstate parents = %s %s", p1, p2)
	}
	if got := r.GetWorkingDirBranch(); got != "feature" {
		t.Fatalf("working dir branch = %s", got)
	}

	tracked, err := r.GetAllTrackedFiles()
	if err != nil {
		t.Fatal(err)
	}
	trackedSet := make(map[string]bool)
	for _, p := range tracked {
		trackedSet[p] = true
	}
	for _, p := range []string{".hgtags", "a.txt", "f.txt", "src/main.go", "src/sub/x.txt"} {
		if !trackedSet[p] {
			t.Fatalf("tracked files lack %s: %v", p, tracked)
		}
	}
}

func TestDeltaChains(t *testing.T) {
	repo := newHgRepo(t, "repo")
	var b strings.Builder
	for i := 0; i < 600; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%20))
		b.WriteString("\n")
	}
	writeFile(t, repo, "big.txt", b.String())
	runHg(t, repo, "add")
	runHg(t, repo, "commit", "-m", "add big file")

	for round := 0; round < 5; round++ {
		lines := strings.Split(b.String(), "\n")
		lines[10+round] = "changed in round " + string(rune('0'+round))
		b.Reset()
		b.WriteString(strings.Join(lines, "\n"))
		writeFile(t, repo, "big.txt", b.String())
		runHg(t, repo, "commit", "-m", "change round")
	}

	r := mustOpen(t, repo)
	filelog, err := r.OpenFilelog("big.txt")
	if err != nil {
		t.Fatal(err)
	}
	sawDelta := false
	for i := range filelog.Entries {
		if filelog.Entries[i].BaseRev != int32(i) {
			sawDelta = true
			break
		}
	}
	if !sawDelta {
		t.Fatalf("the filelog has no delta revision; the test is not exercising chain resolution")
	}
	for rev := 0; rev < 6; rev++ {
		c, err := r.GetCommitByRev(rev)
		if err != nil {
			t.Fatal(err)
		}
		want := runHg(t, repo, "cat", "-r", string(rune('0'+rev)), "--", "big.txt")
		got, err := r.GetFileContent(c, "big.txt")
		if err != nil {
			t.Fatalf("rev %d: %s", rev, err)
		}
		if string(got) != want {
			t.Fatalf("rev %d content differs (%d vs %d bytes)", rev, len(got), len(want))
		}
		manifestOut := runHg(t, repo, "manifest", "--debug", "-r", string(rune('0'+rev)))
		wantCount := len(strings.Split(strings.TrimSpace(manifestOut), "\n"))
		m, err := r.GetManifest(c)
		if err != nil {
			t.Fatalf("rev %d manifest: %s", rev, err)
		}
		if len(m.ItemList) != wantCount {
			t.Fatalf("rev %d manifest has %d entries, hg has %d", rev, len(m.ItemList), wantCount)
		}
	}
}

func TestPathEncoding(t *testing.T) {
	repo := newHgRepo(t, "repo")
	longDir := strings.Repeat("long-directory-name/", 10)
	paths := []string{
		"src/" + longDir + "file.txt",
		"UPPER/Case.TXT",
		"with space.txt",
		".hidden",
		"aux.txt",
		"con",
		"日本語.txt",
		"src/normal.txt",
	}
	for i, p := range paths {
		writeFile(t, repo, p, "content "+string(rune('a'+i))+"\n")
	}
	runHg(t, repo, "add")
	runHg(t, repo, "commit", "-m", "add files")

	r := mustOpen(t, repo)
	c, err := r.GetTip()
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range paths {
		want := runHg(t, repo, "cat", "-r", "0", "--", p)
		got, err := r.GetFileContent(c, p)
		if err != nil {
			t.Fatalf("%s: %s", p, err)
		}
		if string(got) != want {
			t.Fatalf("%s: content differs: %q vs %q", p, got, want)
		}
		if !r.HasFilelog(p) {
			t.Fatalf("%s: no filelog", p)
		}
		_ = i
	}
	tracked, err := r.GetAllTrackedFiles()
	if err != nil {
		t.Fatal(err)
	}
	set := make(map[string]bool)
	for _, p := range tracked {
		set[p] = true
	}
	for _, p := range paths {
		if !set[p] {
			t.Fatalf("tracked files lack %s: %v", p, tracked)
		}
	}
}

func TestEmptyRepository(t *testing.T) {
	repo := newHgRepo(t, "empty")
	r := mustOpen(t, repo)
	if !r.IsEmpty() {
		t.Fatal("new repository should be empty")
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
	bookmarks, err := r.GetAllBookmarkList()
	if err != nil {
		t.Fatal(err)
	}
	if len(bookmarks) != 0 {
		t.Fatalf("bookmarks = %v", bookmarks)
	}
	d, err := r.GetDirstate()
	if err != nil {
		t.Fatal(err)
	}
	if d.P1 != nullNodeHex || d.P2 != nullNodeHex {
		t.Fatalf("dirstate parents = %s %s", d.P1, d.P2)
	}
	tracked, err := r.GetAllTrackedFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(tracked) != 0 {
		t.Fatalf("tracked = %v", tracked)
	}
}

func TestConfig(t *testing.T) {
	repo := newHgRepo(t, "repo")
	hgrc := `[web]
description = a test repository
[ui]
username = Config User <config@example.com>
[hooks]
changegroup = echo hi
`
	if err := os.WriteFile(filepath.Join(repo, ".hg", "hgrc"), []byte(hgrc), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".hg", "extra.rc"), []byte("[web]\ncontact = someone\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(repo, ".hg", "hgrc"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("%include extra.rc\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	r := mustOpen(t, repo)
	if r.Description != "a test repository" {
		t.Fatalf("description = %q", r.Description)
	}
	if r.DefaultUser != "Config User <config@example.com>" {
		t.Fatalf("default user = %q", r.DefaultUser)
	}
	if v, ok := r.Config.Get("web", "contact"); !ok || v != "someone" {
		t.Fatalf("included config = %q, %v", v, ok)
	}
	if v, ok := r.Config.Get("hooks", "changegroup"); !ok || v != "echo hi" {
		t.Fatalf("hook config = %q, %v", v, ok)
	}
}

func TestFileMetadataFilter(t *testing.T) {
	plain := []byte("hello\n")
	content, meta := FilterFileMetadata(plain)
	if !bytes.Equal(content, plain) || meta != nil {
		t.Fatalf("plain = %q, %v", content, meta)
	}

	packed := PackFileMetadata(map[string]string{"copy": "old.txt", "copyrev": nullNodeHex}, plain)
	content, meta = FilterFileMetadata(packed)
	if !bytes.Equal(content, plain) {
		t.Fatalf("content = %q", content)
	}
	if meta["copy"] != "old.txt" || meta["copyrev"] != nullNodeHex {
		t.Fatalf("meta = %v", meta)
	}

	// A file whose own content starts with the metadata marker.
	special := []byte("\x01\nthis is content\n")
	packed = PackFileMetadata(nil, special)
	content, meta = FilterFileMetadata(packed)
	if !bytes.Equal(content, special) {
		t.Fatalf("special content = %q", content)
	}
	if len(meta) != 0 {
		t.Fatalf("special meta = %v", meta)
	}
}
