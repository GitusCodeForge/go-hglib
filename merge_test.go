package hglib

import (
	"errors"
	"testing"
)

func TestCheckAndMergeBranches(t *testing.T) {
	repo := newHgRepo(t, "repo")
	writeFile(t, repo, "base.txt", "base\n")
	runHg(t, repo, "add")
	runHg(t, repo, "commit", "-m", "base")

	runHg(t, repo, "branch", "left")
	writeFile(t, repo, "left.txt", "left\n")
	runHg(t, repo, "add", "left.txt")
	runHg(t, repo, "commit", "-m", "left work")

	runHg(t, repo, "update", "-r", "0")
	runHg(t, repo, "branch", "right")
	writeFile(t, repo, "right.txt", "right\n")
	runHg(t, repo, "add", "right.txt")
	runHg(t, repo, "commit", "-m", "right work")

	r := mustOpen(t, repo)
	check, err := r.CheckBranchMergeConflict("left", "right")
	if err != nil {
		t.Fatal(err)
	}
	if !check.Successful {
		t.Fatalf("expected a clean merge: %+v", check)
	}
	if check.ToplevelTreeOid == "" {
		t.Fatalf("clean merge has no result manifest: %+v", check)
	}

	if err := r.Merge("right", "left", "Merger", "merger@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := r.Refresh(); err != nil {
		t.Fatal(err)
	}
	branches, err := r.GetAllBranchList()
	if err != nil {
		t.Fatal(err)
	}
	left := branches["left"]
	if left == nil {
		t.Fatal("left branch disappeared")
	}
	mergeCommit, err := r.GetCommit(left.HeadId)
	if err != nil {
		t.Fatal(err)
	}
	if mergeCommit.P2 < 0 {
		t.Fatalf("left head is not a merge: %+v", mergeCommit)
	}
	if mergeCommit.Branch != "left" {
		t.Fatalf("merge branch = %s", mergeCommit.Branch)
	}
	for _, p := range []string{"base.txt", "left.txt", "right.txt"} {
		if _, err := r.GetFileContent(mergeCommit, p); err != nil {
			t.Fatalf("%s after merge: %s", p, err)
		}
	}
	// Now right is an ancestor of left's head.
	check, err = r.CheckBranchMergeConflict("left", "right")
	if err != nil {
		t.Fatal(err)
	}
	if !check.Successful {
		t.Fatalf("merged branches still report a conflict: %+v", check)
	}
}

func TestMergeConflict(t *testing.T) {
	repo := newHgRepo(t, "repo")
	writeFile(t, repo, "f.txt", "base\n")
	runHg(t, repo, "add")
	runHg(t, repo, "commit", "-m", "base")

	runHg(t, repo, "branch", "left")
	writeFile(t, repo, "f.txt", "left\n")
	runHg(t, repo, "commit", "-m", "left work")

	runHg(t, repo, "update", "-r", "0")
	runHg(t, repo, "branch", "right")
	writeFile(t, repo, "f.txt", "right\n")
	runHg(t, repo, "commit", "-m", "right work")

	r := mustOpen(t, repo)
	check, err := r.CheckBranchMergeConflict("left", "right")
	if err != nil {
		t.Fatal(err)
	}
	if check.Successful {
		t.Fatalf("expected a conflict: %+v", check)
	}
	if len(check.FileInfo) != 1 || check.FileInfo[0].FileName != "f.txt" {
		t.Fatalf("conflicted files = %+v", check.FileInfo)
	}

	err = r.Merge("right", "left", "Merger", "merger@example.com")
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("Merge error = %v, want ErrMergeConflict", err)
	}
	// The original repository must be untouched.
	branches, err := r.GetAllBranchList()
	if err != nil {
		t.Fatal(err)
	}
	left, err := r.GetCommit(branches["left"].HeadId)
	if err != nil {
		t.Fatal(err)
	}
	if left.P2 >= 0 {
		t.Fatalf("a failed merge created a merge changeset: %+v", left)
	}
}
