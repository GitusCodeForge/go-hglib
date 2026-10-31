package hglib

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrMergeConflict = errors.New("merge has conflicts")

// MergeCheckConflictedFileInfo describes one file left unresolved by a merge.
// Mercurial has no index stages, so Stage is always zero and Mode/ObjectId are
// left empty; FileName is the useful field.
type MergeCheckConflictedFileInfo struct {
	Mode     int    `json:"mode"`
	ObjectId string `json:"oid"`
	Stage    int    `json:"stage"`
	FileName string `json:"fileName"`
}

// MergeCheckInformationalMessage is one message produced while checking a
// merge.
type MergeCheckInformationalMessage struct {
	Path    []string `json:"path"`
	Type    string   `json:"type"`
	Message string   `json:"msg"`
}

// MergeCheckResult is the outcome of CheckBranchMergeConflict.
type MergeCheckResult struct {
	Successful         bool                             `json:"success"`
	ReceiverLocation   string                           `json:"receiverLocation"`
	ReceiverBranch     string                           `json:"receiverBranch"`
	ProviderRemoteName string                           `json:"providerRemoteName"`
	ProviderBranch     string                           `json:"providerBranch"`
	ToplevelTreeOid    string                           `json:"rootOid"`
	FileInfo           []MergeCheckConflictedFileInfo   `json:"fileInfo"`
	Message            []MergeCheckInformationalMessage `json:"msg"`
}

func runHgCommandStatus(dir string, args ...string) (stdout string, stderr string, exitCode int, err error) {
	cmd := hgCommand(args...)
	cmd.Dir = dir
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return outBuf.String(), errBuf.String(), exitErr.ExitCode(), nil
		}
		return outBuf.String(), errBuf.String(), -1, err
	}
	return outBuf.String(), errBuf.String(), 0, nil
}

func (r *LocalHgRepository) branchHeads(localBranch, otherBranch string) (*Branch, *Branch, error) {
	branches, err := r.GetAllBranchList()
	if err != nil {
		return nil, nil, err
	}
	local, ok := branches[localBranch]
	if !ok {
		return nil, nil, fmt.Errorf("branch %q does not exist", localBranch)
	}
	other, ok := branches[otherBranch]
	if !ok {
		return nil, nil, fmt.Errorf("branch %q does not exist", otherBranch)
	}
	return local, other, nil
}

func (r *LocalHgRepository) isAncestor(ancestorNode, descendantNode string) (bool, error) {
	out, err := runHgCommand(r.RootPath, "log", "-r",
		fmt.Sprintf("ancestor(%s,%s)", ancestorNode, descendantNode),
		"-T", "{node}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == ancestorNode, nil
}

// CheckBranchMergeConflict merges otherBranch into localBranch in a temporary
// clone and reports whether the merge is clean. Neither the repository nor its
// working directory is modified.
func (r *LocalHgRepository) CheckBranchMergeConflict(localBranch, otherBranch string) (*MergeCheckResult, error) {
	local, other, err := r.branchHeads(localBranch, otherBranch)
	if err != nil {
		return nil, err
	}
	res := &MergeCheckResult{
		ReceiverLocation: r.RootPath,
		ReceiverBranch:   localBranch,
		ProviderBranch:   otherBranch,
		FileInfo:         make([]MergeCheckConflictedFileInfo, 0),
		Message:          make([]MergeCheckInformationalMessage, 0),
	}

	// If the other head is already an ancestor of the local head, there is
	// nothing to merge.
	if anc, err := r.isAncestor(other.HeadId, local.HeadId); err != nil {
		return nil, err
	} else if anc {
		res.Successful = true
		if c, err := r.GetCommit(local.HeadId); err == nil {
			res.ToplevelTreeOid = c.TreeObjId
		}
		return res, nil
	}
	// If the local head is an ancestor of the other head, the merge is
	// trivially clean and its tree is the other head's manifest. This is a
	// fast-forward only when the two names are the same branch; when they
	// differ the merge still has to be recorded, but there can be no conflict.
	if anc, err := r.isAncestor(local.HeadId, other.HeadId); err != nil {
		return nil, err
	} else if anc {
		res.Successful = true
		if c, err := r.GetCommit(other.HeadId); err == nil {
			res.ToplevelTreeOid = c.TreeObjId
		}
		return res, nil
	}

	tmp, err := os.MkdirTemp("", "go-hglib-mergecheck-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	cloneDir := filepath.Join(tmp, "repo")
	if _, err := runHgCommand(tmp, "clone", "-U", r.RootPath, cloneDir); err != nil {
		return nil, err
	}
	if _, err := runHgCommand(cloneDir, "update", "-r", local.HeadId); err != nil {
		return nil, err
	}
	_, mergeErr, exitCode, err := runHgCommandStatus(cloneDir, "merge", "-r", other.HeadId, "--tool", "internal:merge3")
	if err != nil {
		return nil, err
	}
	resolveOut, _, _, resolveErr := runHgCommandStatus(cloneDir, "resolve", "-l")
	if resolveErr != nil {
		return nil, resolveErr
	}
	for _, line := range strings.Split(resolveOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		state, path, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if state == "U" {
			res.FileInfo = append(res.FileInfo, MergeCheckConflictedFileInfo{FileName: path})
		}
	}
	res.Successful = exitCode == 0 && len(res.FileInfo) == 0
	if !res.Successful {
		if strings.TrimSpace(mergeErr) != "" {
			res.Message = append(res.Message, MergeCheckInformationalMessage{
				Type:    "merge",
				Message: strings.TrimSpace(mergeErr),
			})
		}
		return res, nil
	}

	// A clean merge has a well-defined result manifest; ask the temporary
	// clone for it instead of recomputing the merge ourselves.
	if _, err := runHgCommand(cloneDir, "commit", "-u", "merge check <merge-check@example.invalid>", "-m", "merge check"); err == nil {
		if out, err := runHgCommand(cloneDir, "log", "-r", ".", "-T", `{manifest % "{node}"}`); err == nil {
			res.ToplevelTreeOid = strings.TrimSpace(out)
		}
	}
	return res, nil
}

// Merge merges otherBranch into localBranch and pushes the resulting changeset
// into the repository. It uses a temporary clone, so the repository's working
// directory is never touched.
func (r *LocalHgRepository) Merge(otherBranch, localBranch, author, email string) error {
	local, other, err := r.branchHeads(localBranch, otherBranch)
	if err != nil {
		return err
	}
	if !validUserPart(author) || !validUserPart(email) {
		return errors.New("invalid merge author")
	}
	user := formatHgUser(author, email)
	if user == "" {
		user = r.DefaultUser
	}

	if anc, err := r.isAncestor(other.HeadId, local.HeadId); err != nil {
		return err
	} else if anc {
		return nil
	}
	// The local branch is an ancestor of the other head. When both names refer
	// to the same branch this is a fast-forward and there is nothing to do.
	// Different branch names still need a merge changeset.
	if localBranch == otherBranch {
		if anc, err := r.isAncestor(local.HeadId, other.HeadId); err != nil {
			return err
		} else if anc {
			return nil
		}
	}

	tmp, err := os.MkdirTemp("", "go-hglib-merge-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	cloneDir := filepath.Join(tmp, "repo")
	if _, err := runHgCommand(tmp, "clone", "-U", r.RootPath, cloneDir); err != nil {
		return err
	}
	if _, err := runHgCommand(cloneDir, "update", "-r", local.HeadId); err != nil {
		return err
	}
	stdout, stderr, exitCode, err := runHgCommandStatus(cloneDir, "merge", "-r", other.HeadId, "--tool", "internal:merge3")
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("%w: %s", ErrMergeConflict, strings.TrimSpace(stderr+stdout))
	}
	resolveOut, _, _, err := runHgCommandStatus(cloneDir, "resolve", "-l")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(resolveOut, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "U ") {
			return fmt.Errorf("%w: %s", ErrMergeConflict, strings.TrimSpace(line))
		}
	}
	commitArgs := []string{"commit", "-m", fmt.Sprintf("merge: %s into %s", otherBranch, localBranch)}
	if user != "" {
		commitArgs = append(commitArgs, "-u", user)
	}
	if _, err := runHgCommand(cloneDir, commitArgs...); err != nil {
		return err
	}
	if _, err := runHgCommand(cloneDir, "push", "-f", "-r", ".", r.RootPath); err != nil {
		return err
	}
	if err := r.Refresh(); err != nil {
		return err
	}
	_ = r.SyncAllBranchList()
	return nil
}
