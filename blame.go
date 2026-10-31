package hglib

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// PorcelainBlameCommitInfo is the changeset information annotation reports for
// one line.
type PorcelainBlameCommitInfo struct {
	CommitId      string
	AuthorInfo    AuthorTime
	CommitterInfo AuthorTime
	Summary       string
	Filename      string
}

// PorcelainBlameLine is one annotated line.
type PorcelainBlameLine struct {
	CommitId        string
	Value           string
	FinalLineNumber int
}

// PorcelainBlame is the result of annotating a file. LineList groups
// consecutive lines that come from the same changeset.
type PorcelainBlame struct {
	CommitInfo map[string]*PorcelainBlameCommitInfo
	LineList   [][]*PorcelainBlameLine
}

type annotateLineJSON struct {
	Date []float64 `json:"date"`
	Line string    `json:"line"`
	Node string    `json:"node"`
	Rev  int       `json:"rev"`
	User string    `json:"user"`
}

type annotateFileJSON struct {
	Lines []annotateLineJSON `json:"lines"`
	Path  string             `json:"path"`
}

// Blame annotates a file at a changeset. Mercurial's annotation engine is
// invoked through hg annotate; the result is converted to the same shape as
// gitlib's porcelain blame.
func (r *LocalHgRepository) Blame(c *CommitObject, filePath string) (*PorcelainBlame, error) {
	if c == nil {
		return nil, fmt.Errorf("no changeset given")
	}
	return r.BlameAtRev(c.Rev, filePath)
}

// BlameAtRev is Blame for a changelog revision number.
func (r *LocalHgRepository) BlameAtRev(rev int, filePath string) (*PorcelainBlame, error) {
	cmd := hgCommand("annotate",
		"-r", strconv.Itoa(rev),
		"-T", "json",
		"-n", "-c", "-u", "-d",
		"--", filePath)
	cmd.Dir = r.RootPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("hg annotate: %s: %s", err, strings.TrimSpace(stderr.String()))
	}
	var files []annotateFileJSON
	if err := json.Unmarshal(stdout.Bytes(), &files); err != nil {
		return nil, fmt.Errorf("hg annotate returned unexpected JSON: %w", err)
	}
	res := &PorcelainBlame{
		CommitInfo: make(map[string]*PorcelainBlameCommitInfo),
		LineList:   make([][]*PorcelainBlameLine, 0),
	}
	if len(files) == 0 {
		return res, nil
	}
	groups := make([][]*PorcelainBlameLine, 0)
	var current []*PorcelainBlameLine
	lastCommit := ""
	for i, line := range files[0].Lines {
		node := line.Node
		if node == "" {
			// Older Mercurial versions may omit -c output; fall back to the
			// revision number.
			if line.Rev >= 0 && line.Rev < r.Changelog.Len() {
				node = r.Changelog.Entries[line.Rev].Node
			}
		}
		if node == "" {
			node = "unknown"
		}
		info, ok := res.CommitInfo[node]
		if !ok {
			info = &PorcelainBlameCommitInfo{
				CommitId: node,
				Filename: filePath,
			}
			if c, err := r.GetCommit(node); err == nil {
				info.AuthorInfo = c.AuthorInfo
				info.CommitterInfo = c.CommitterInfo
				info.Summary = firstLine(c.CommitMessage)
			} else if line.Rev >= 0 && line.Rev < r.Changelog.Len() {
				if c, err := r.GetCommitByRev(line.Rev); err == nil {
					info.AuthorInfo = c.AuthorInfo
					info.CommitterInfo = c.CommitterInfo
					info.Summary = firstLine(c.CommitMessage)
				}
			}
			if info.AuthorInfo.AuthorName == "" && info.AuthorInfo.AuthorEmail == "" {
				u := parseUser(line.User)
				info.AuthorInfo = u
				info.CommitterInfo = u
				if len(line.Date) >= 2 {
					info.AuthorInfo.Time = parseHgDate(
						strconv.FormatFloat(line.Date[0], 'f', -1, 64),
						strconv.Itoa(int(line.Date[1])))
					info.CommitterInfo.Time = info.AuthorInfo.Time
				}
			}
			res.CommitInfo[node] = info
		}
		pl := &PorcelainBlameLine{
			CommitId:        node,
			Value:           line.Line,
			FinalLineNumber: i + 1,
		}
		if node != lastCommit {
			if len(current) > 0 {
				groups = append(groups, current)
			}
			current = make([]*PorcelainBlameLine, 0)
			lastCommit = node
		}
		current = append(current, pl)
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	res.LineList = groups
	return res, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
