package hglib

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var REGEX_VALID_BRANCH_NAME = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func validBranchName(s string) bool {
	return REGEX_VALID_BRANCH_NAME.MatchString(s)
}

func validRepoPath(s string) bool {
	if s == "" || strings.ContainsAny(s, "\r\n\x00") {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(s))
	if clean == ".hg" || strings.HasPrefix(clean, ".hg"+string(filepath.Separator)) {
		return false
	}
	return filepath.IsLocal(clean)
}

func validUserPart(s string) bool {
	return !strings.ContainsAny(s, "\r\n\x00")
}

func formatHgUser(name, email string) string {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	switch {
	case name != "" && email != "":
		return fmt.Sprintf("%s <%s>", name, email)
	case email != "":
		return email
	default:
		return name
	}
}

// AddFileToRepoString commits one file to a branch. Mercurial has no
// fast-import equivalent that can write into a store without a working
// directory, so this creates a temporary clone at the branch head, commits
// there and pushes the new changeset back. The returned value is the new
// changeset node.
func (r *LocalHgRepository) AddFileToRepoString(
	branchName string, filePath string,
	authorName string, authorEmail string,
	committerName string, committerEmail string,
	commitMessage string,
	content string,
) (string, error) {
	return r.addFilesToRepo(branchName, map[string]string{filePath: content},
		authorName, authorEmail, committerName, committerEmail, commitMessage)
}

// AddFileToRepoReader is AddFileToRepoString for a stream. The contentSize
// argument is accepted for symmetry with gitlib; Mercurial does not need it.
func (r *LocalHgRepository) AddFileToRepoReader(
	branchName string, filePath string,
	authorName string, authorEmail string,
	committerName string, committerEmail string,
	commitMessage string,
	content io.Reader, contentSize int64,
) (string, error) {
	if !validRepoPath(filePath) {
		return "", errors.New("invalid file path")
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return "", err
	}
	return r.addFilesToRepo(branchName, map[string]string{filePath: string(data)},
		authorName, authorEmail, committerName, committerEmail, commitMessage)
}

// AddMultipleFileToRepoString commits several files at once. Paths are
// repository-root-relative.
func (r *LocalHgRepository) AddMultipleFileToRepoString(
	branchName string,
	authorName string, authorEmail string,
	committerName string, committerEmail string,
	commitMessage string,
	content map[string]string,
) (string, error) {
	return r.addFilesToRepo(branchName, content,
		authorName, authorEmail, committerName, committerEmail, commitMessage)
}

func (r *LocalHgRepository) addFilesToRepo(
	branchName string,
	files map[string]string,
	authorName, authorEmail, committerName, committerEmail, commitMessage string,
) (string, error) {
	if !validBranchName(branchName) {
		return "", errors.New("invalid branch name")
	}
	if len(files) == 0 {
		return "", errors.New("no files to commit")
	}
	for p := range files {
		if !validRepoPath(p) {
			return "", fmt.Errorf("invalid file path: %s", p)
		}
	}
	for _, s := range []string{authorName, authorEmail, committerName, committerEmail, commitMessage} {
		if !validUserPart(s) {
			return "", errors.New("commit metadata contains a newline")
		}
	}
	user := formatHgUser(authorName, authorEmail)
	if user == "" {
		user = formatHgUser(committerName, committerEmail)
	}
	if user == "" {
		user = r.DefaultUser
	}

	// Find the base revision. A branch that does not exist yet is started at
	// the tip, which matches how a new Mercurial branch is normally created.
	baseRev := ""
	branches, err := r.GetAllBranchList()
	if err != nil {
		return "", err
	}
	if b, ok := branches[branchName]; ok {
		baseRev = b.HeadId
	} else if !r.IsEmpty() {
		tip, err := r.GetTip()
		if err != nil {
			return "", err
		}
		baseRev = tip.Id
	}

	tmp, err := os.MkdirTemp("", "go-hglib-commit-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	cloneDir := filepath.Join(tmp, "repo")

	cloneArgs := []string{"clone", "-U"}
	if baseRev != "" {
		cloneArgs = append(cloneArgs, "-r", baseRev)
	}
	cloneArgs = append(cloneArgs, r.RootPath, cloneDir)
	if _, err := runHgCommand(tmp, cloneArgs...); err != nil {
		return "", err
	}
	if baseRev != "" {
		if _, err := runHgCommand(cloneDir, "update", "-r", baseRev); err != nil {
			return "", err
		}
	}
	if _, exists := branches[branchName]; !exists {
		if _, err := runHgCommand(cloneDir, "branch", branchName); err != nil {
			return "", err
		}
	}

	for p, content := range files {
		full := filepath.Join(cloneDir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			return "", err
		}
	}
	if _, err := runHgCommand(cloneDir, "addremove"); err != nil {
		return "", err
	}

	commitArgs := []string{"commit", "-m", commitMessage}
	if user != "" {
		commitArgs = append(commitArgs, "-u", user)
	}
	if _, err := runHgCommand(cloneDir, commitArgs...); err != nil {
		return "", err
	}
	out, err := runHgCommand(cloneDir, "log", "-r", ".", "-T", "{node}")
	if err != nil {
		return "", err
	}
	newNode := strings.TrimSpace(out)
	if !IsValidNode(newNode) {
		return "", fmt.Errorf("hg commit returned an unexpected node id: %q", newNode)
	}

	// A force push is safe here: the new changeset is a child of the branch
	// head we cloned, so this only creates a new head when the original
	// repository already had several.
	if _, err := runHgCommand(cloneDir, "push", "-f", "-r", ".", r.RootPath); err != nil {
		return "", err
	}
	if err := r.Refresh(); err != nil {
		return "", err
	}
	_ = r.SyncAllBranchList()
	return newNode, nil
}

func runHgCommand(dir string, args ...string) (string, error) {
	cmd := hgCommand(args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("hg %s: %s: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
