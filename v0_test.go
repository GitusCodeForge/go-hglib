package hglib

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

type v0Revision struct {
	Text []byte
	Base int32
	Link int32
	P1   string
	P2   string
	Node string
}

// writeV0Revlog writes an original-format (version 0) revlog: a 76-byte index
// entry per revision and a separate .d file with zlib chunks.
func writeV0Revlog(t *testing.T, indexPath, dataPath string, revs []v0Revision) {
	t.Helper()
	var data bytes.Buffer
	var index bytes.Buffer
	for i, rev := range revs {
		var compressed bytes.Buffer
		zw := zlib.NewWriter(&compressed)
		if _, err := zw.Write(rev.Text); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		offset := data.Len()
		data.Write(compressed.Bytes())

		p1, err := hexToBytes(rev.P1)
		if err != nil || len(p1) != 20 {
			p1 = nullNodeBytes
		}
		p2, err := hexToBytes(rev.P2)
		if err != nil || len(p2) != 20 {
			p2 = nullNodeBytes
		}
		node, err := hexToBytes(rev.Node)
		if err != nil || len(node) != 20 {
			t.Fatalf("rev %d: bad node %q", i, rev.Node)
		}
		var entry [76]byte
		binary.BigEndian.PutUint32(entry[0:4], uint32(offset))
		binary.BigEndian.PutUint32(entry[4:8], uint32(compressed.Len()))
		binary.BigEndian.PutUint32(entry[8:12], uint32(rev.Base))
		binary.BigEndian.PutUint32(entry[12:16], uint32(rev.Link))
		copy(entry[16:36], p1)
		copy(entry[36:56], p2)
		copy(entry[56:76], node)
		index.Write(entry[:])
	}
	if err := os.MkdirAll(filepath.Dir(dataPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, data.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, index.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestVersion0Repository(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "old")
	hg := filepath.Join(repo, ".hg")
	if err := os.MkdirAll(filepath.Join(hg, "data"), 0755); err != nil {
		t.Fatal(err)
	}

	content0 := []byte("hello\n")
	content1 := []byte("hello world\n")
	fileNode0 := HashRevision(content0, nullNodeHex, nullNodeHex)
	fileNode1 := HashRevision(content1, fileNode0, nullNodeHex)

	writeV0Revlog(t,
		filepath.Join(hg, "data", "a.txt.i"),
		filepath.Join(hg, "data", "a.txt.d"),
		[]v0Revision{
			{Text: content0, Base: 0, Link: 0, Node: fileNode0},
			{Text: content1, Base: 1, Link: 1, P1: fileNode0, Node: fileNode1},
		})

	manifest0 := []byte("a.txt\x00" + fileNode0 + "\n")
	manifest1 := []byte("a.txt\x00" + fileNode1 + "\n")
	manifestNode0 := HashRevision(manifest0, nullNodeHex, nullNodeHex)
	manifestNode1 := HashRevision(manifest1, manifestNode0, nullNodeHex)
	writeV0Revlog(t,
		filepath.Join(hg, "00manifest.i"),
		filepath.Join(hg, "00manifest.d"),
		[]v0Revision{
			{Text: manifest0, Base: 0, Link: 0, Node: manifestNode0},
			{Text: manifest1, Base: 1, Link: 1, P1: manifestNode0, Node: manifestNode1},
		})

	changelog0 := []byte(manifestNode0 + "\nTest User <test@example.com>\n1700000000 0\n\ninitial\n")
	changelog1 := []byte(manifestNode1 + "\nTest User <test@example.com>\n1700000100 0\na.txt\n\nsecond\n")
	changelogNode0 := HashRevision(changelog0, nullNodeHex, nullNodeHex)
	changelogNode1 := HashRevision(changelog1, changelogNode0, nullNodeHex)
	writeV0Revlog(t,
		filepath.Join(hg, "00changelog.i"),
		filepath.Join(hg, "00changelog.d"),
		[]v0Revision{
			{Text: changelog0, Base: 0, Link: 0, Node: changelogNode0},
			{Text: changelog1, Base: 1, Link: 1, P1: changelogNode0, Node: changelogNode1},
		})

	r := mustOpen(t, repo)
	if r.Changelog.FormatVersion != revlogV0 {
		t.Fatalf("changelog version = %d", r.Changelog.FormatVersion)
	}
	if r.Changelog.GeneralDelta {
		t.Fatal("version 0 revlog must not report generaldelta")
	}
	tip, err := r.GetTip()
	if err != nil {
		t.Fatal(err)
	}
	if tip.Id != changelogNode1 || tip.TreeObjId != manifestNode1 {
		t.Fatalf("tip = %+v", tip)
	}
	if tip.P1 != 0 || tip.P2 != -1 {
		t.Fatalf("tip parents = %d, %d", tip.P1, tip.P2)
	}
	if tip.CommitMessage != "second\n" {
		t.Fatalf("tip message = %q", tip.CommitMessage)
	}
	if len(tip.Files) != 1 || tip.Files[0] != "a.txt" {
		t.Fatalf("tip files = %v", tip.Files)
	}

	hist, err := r.GetCommitHistoryN("tip", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].Id != changelogNode1 || hist[1].Id != changelogNode0 {
		t.Fatalf("history = %+v", hist)
	}

	rev0, err := r.GetCommitByRev(0)
	if err != nil {
		t.Fatal(err)
	}
	got0, err := r.GetFileContent(rev0, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got0) != string(content0) {
		t.Fatalf("rev 0 content = %q", got0)
	}
	got1, err := r.GetFileContent(tip, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got1) != string(content1) {
		t.Fatalf("rev 1 content = %q", got1)
	}

	m, err := r.GetManifest(tip)
	if err != nil {
		t.Fatal(err)
	}
	item, ok := m.Get("a.txt")
	if !ok || item.Node != fileNode1 {
		t.Fatalf("manifest item = %+v, %v", item, ok)
	}

	tracked, err := r.GetAllTrackedFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(tracked) != 1 || tracked[0] != "a.txt" {
		t.Fatalf("tracked = %v", tracked)
	}

	branches, err := r.GetAllBranchList()
	if err != nil {
		t.Fatal(err)
	}
	if b := branches["default"]; b == nil || b.HeadId != changelogNode1 {
		t.Fatalf("branches = %+v", branches)
	}
}
