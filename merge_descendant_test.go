package hglib

import "testing"

// A branch that is an ancestor of another branch can still be the receiver of
// a merge changeset. Mercurial records it rather than fast-forwarding because
// named branch heads are not movable pointers.
func TestMergeDescendantBranch(t *testing.T) {
	repo := newHgRepo(t, "repo")
	writeFile(t, repo, "base.txt", "base\n")
	runHg(t, repo, "add")
	runHg(t, repo, "commit", "-m", "base")

	runHg(t, repo, "branch", "left")
	writeFile(t, repo, "left.txt", "left\n")
	runHg(t, repo, "add", "left.txt")
	runHg(t, repo, "commit", "-m", "left")

	// right is a child of left, not a sibling.
	runHg(t, repo, "branch", "right")
	writeFile(t, repo, "right.txt", "right\n")
	runHg(t, repo, "add", "right.txt")
	runHg(t, repo, "commit", "-m", "right")

	r := mustOpen(t, repo)
	before, err := r.GetAllBranchList()
	if err != nil {
		t.Fatal(err)
	}
	leftBefore, err := r.GetCommit(before["left"].HeadId)
	if err != nil {
		t.Fatal(err)
	}
	right, err := r.GetCommit(before["right"].HeadId)
	if err != nil {
		t.Fatal(err)
	}

	check, err := r.CheckBranchMergeConflict("left", "right")
	if err != nil {
		t.Fatal(err)
	}
	if !check.Successful {
		t.Fatalf("descendant merge should be clean: %+v", check)
	}
	if check.ToplevelTreeOid != right.TreeObjId {
		t.Fatalf("merge tree = %s, want %s", check.ToplevelTreeOid, right.TreeObjId)
	}

	if err := r.Merge("right", "left", "Merger", "merger@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := r.Refresh(); err != nil {
		t.Fatal(err)
	}
	after, err := r.GetAllBranchList()
	if err != nil {
		t.Fatal(err)
	}
	merged, err := r.GetCommit(after["left"].HeadId)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Id == leftBefore.Id {
		t.Fatalf("left head did not change")
	}
	if merged.P1 != leftBefore.Rev || merged.P2 != right.Rev {
		t.Fatalf("merge parents = %d, %d; want %d, %d", merged.P1, merged.P2, leftBefore.Rev, right.Rev)
	}
	if merged.Branch != "left" {
		t.Fatalf("merge branch = %s", merged.Branch)
	}
}
