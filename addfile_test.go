package hglib

import (
	"strings"
	"testing"
)

func TestAddFileToRepo(t *testing.T) {
	repo := newHgRepo(t, "repo")
	writeFile(t, repo, "a.txt", "one\n")
	runHg(t, repo, "add")
	runHg(t, repo, "commit", "-m", "initial")

	r := mustOpen(t, repo)
	node, err := r.AddFileToRepoString(
		"default", "new.txt",
		"Committer", "committer@example.com",
		"Committer", "committer@example.com",
		"add new file", "two\n")
	if err != nil {
		t.Fatal(err)
	}
	if !IsValidNode(node) {
		t.Fatalf("node = %q", node)
	}
	if err := r.Refresh(); err != nil {
		t.Fatal(err)
	}
	c, err := r.GetCommit(node)
	if err != nil {
		t.Fatal(err)
	}
	if c.Branch != "default" || !strings.HasPrefix(c.CommitMessage, "add new file") {
		t.Fatalf("commit = %+v", c)
	}
	if c.AuthorInfo.AuthorName != "Committer" || c.AuthorInfo.AuthorEmail != "committer@example.com" {
		t.Fatalf("author = %+v", c.AuthorInfo)
	}
	content, err := r.GetFileContent(c, "new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "two\n" {
		t.Fatalf("content = %q", content)
	}

	// A branch that does not exist starts at the tip.
	node2, err := r.AddFileToRepoString(
		"feature", "feature.txt",
		"Committer", "committer@example.com",
		"Committer", "committer@example.com",
		"start feature", "feature\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Refresh(); err != nil {
		t.Fatal(err)
	}
	c2, err := r.GetCommit(node2)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Branch != "feature" {
		t.Fatalf("branch = %s", c2.Branch)
	}
	if _, err := r.GetFileContent(c2, "new.txt"); err != nil {
		t.Fatalf("feature commit should inherit new.txt: %s", err)
	}

	// Several files in one commit.
	node3, err := r.AddMultipleFileToRepoString(
		"default", "Committer", "committer@example.com",
		"Committer", "committer@example.com",
		"add two files",
		map[string]string{"x/y.txt": "x\n", "z.txt": "z\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Refresh(); err != nil {
		t.Fatal(err)
	}
	c3, err := r.GetCommit(node3)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"x/y.txt": "x\n", "z.txt": "z\n"} {
		got, err := r.GetFileContent(c3, p)
		if err != nil {
			t.Fatalf("%s: %s", p, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", p, got, want)
		}
	}
}

func TestAddFileToEmptyRepo(t *testing.T) {
	repo := newHgRepo(t, "empty")
	r := mustOpen(t, repo)
	if !r.IsEmpty() {
		t.Fatal("repository should be empty")
	}
	node, err := r.AddFileToRepoString(
		"default", "first.txt",
		"Committer", "committer@example.com",
		"Committer", "committer@example.com",
		"first commit", "hello\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Refresh(); err != nil {
		t.Fatal(err)
	}
	if r.IsEmpty() {
		t.Fatal("repository should no longer be empty")
	}
	c, err := r.GetCommit(node)
	if err != nil {
		t.Fatal(err)
	}
	if c.Branch != "default" {
		t.Fatalf("branch = %s", c.Branch)
	}
	content, err := r.GetFileContent(c, "first.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello\n" {
		t.Fatalf("content = %q", content)
	}
}
