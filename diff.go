package hglib

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const (
	DIFF_OLD_MODE            uint8 = 1
	DIFF_NEW_MODE            uint8 = 2
	DIFF_DELETED_FILE_MODE   uint8 = 3
	DIFF_NEW_FILE_MODE       uint8 = 4
	DIFF_COPY_FROM           uint8 = 5
	DIFF_COPY_TO             uint8 = 6
	DIFF_RENAME_FROM         uint8 = 7
	DIFF_RENAME_TO           uint8 = 8
	DIFF_SIMILARITY_INDEX    uint8 = 9
	DIFF_DISSIMILARITY_INDEX uint8 = 10
	DIFF_INDEX               uint8 = 11
)

const (
	APPEND uint8 = 1
	DELETE uint8 = 2
	CHANGE uint8 = 3
	SAME   uint8 = 4
)

var ErrInvalidFormat = errors.New("invalid diff format")

// AnnotatedLine is one line of a hunk. F1LineNum refers to the left side and
// F2LineNum to the right side; the counter that does not apply still carries
// the line position at which the other side's change happened.
type AnnotatedLine struct {
	Type      uint8  `json:"type"`
	F1LineNum int64  `json:"f1"`
	F2LineNum int64  `json:"f2"`
	Line      string `json:"line"`
}

// DiffItemHeaderItem is one extended header line of a file diff, such as
// "new file mode 100644".
type DiffItemHeaderItem struct {
	Type uint8    `json:"type"`
	Args []string `json:"args"`
}

// DiffItemPatch is one hunk.
type DiffItemPatch struct {
	LStart      int64           `json:"lStart"`
	LLineCount  int64           `json:"lLineCount"`
	RStart      int64           `json:"rStart"`
	RLineCount  int64           `json:"rLineCount"`
	ContextLine string          `json:"ctx"`
	LineList    []AnnotatedLine `json:"lines"`
}

// DiffItem is the diff of one file.
type DiffItem struct {
	File1     string                `json:"file1"`
	File2     string                `json:"file2"`
	Header    []*DiffItemHeaderItem `json:"header"`
	PatchList []*DiffItemPatch      `json:"patchList"`
}

// Diff is the diff of one changeset against its first parent.
type Diff struct {
	CommitHash string      `json:"commit"`
	ItemList   []*DiffItem `json:"item"`
}

func parseHgDiffHeaderItem(s string) *DiffItemHeaderItem {
	parse := func(kind string, typ uint8) *DiffItemHeaderItem {
		return &DiffItemHeaderItem{Type: typ, Args: []string{strings.TrimSpace(s[len(kind):])}}
	}
	switch {
	case strings.HasPrefix(s, "old mode "):
		return parse("old mode ", DIFF_OLD_MODE)
	case strings.HasPrefix(s, "new mode "):
		return parse("new mode ", DIFF_NEW_MODE)
	case strings.HasPrefix(s, "deleted file mode "):
		return parse("deleted file mode ", DIFF_DELETED_FILE_MODE)
	case strings.HasPrefix(s, "new file mode "):
		return parse("new file mode ", DIFF_NEW_FILE_MODE)
	case strings.HasPrefix(s, "copy from "):
		return parse("copy from ", DIFF_COPY_FROM)
	case strings.HasPrefix(s, "copy to "):
		return parse("copy to ", DIFF_COPY_TO)
	case strings.HasPrefix(s, "rename from "):
		return parse("rename from ", DIFF_RENAME_FROM)
	case strings.HasPrefix(s, "rename to "):
		return parse("rename to ", DIFF_RENAME_TO)
	case strings.HasPrefix(s, "similarity index "):
		return parse("similarity index ", DIFF_SIMILARITY_INDEX)
	case strings.HasPrefix(s, "dissimilarity index "):
		return parse("dissimilarity index ", DIFF_DISSIMILARITY_INDEX)
	case strings.HasPrefix(s, "index "):
		return &DiffItemHeaderItem{Type: DIFF_INDEX, Args: []string{strings.TrimSpace(s[len("index "):])}}
	}
	return nil
}

var reHgDiffHunk = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

func parseHgDiff(stdout []byte) (*Diff, error) {
	lr := newLineReader(bytes.NewBuffer(stdout))
	res := &Diff{ItemList: make([]*DiffItem, 0)}
	for {
		raw, err := lr.readLine()
		if errors.Is(err, io.EOF) && raw == "" {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		line := trimLineEnding(raw)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "diff --git ") {
			// Binary file notices and other chatter do not start a file item.
			continue
		}
		item, err := parseHgDiffItem(lr)
		if err != nil {
			return nil, err
		}
		res.ItemList = append(res.ItemList, item)
	}
	return res, nil
}

func parseHgDiffItem(lr *lineReader) (*DiffItem, error) {
	item := &DiffItem{Header: make([]*DiffItemHeaderItem, 0)}
	var file1, file2 string
	sawFileHeader := false

	// Extended headers, then "--- <left>".
	for {
		raw, err := lr.readLine()
		if errors.Is(err, io.EOF) && raw == "" {
			return item, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		line := trimLineEnding(raw)
		if strings.HasPrefix(line, "diff --git ") {
			lr.unreadLine(raw)
			return item, nil
		}
		if strings.HasPrefix(line, "--- ") {
			file1 = strings.TrimSpace(line[len("--- "):])
			sawFileHeader = true
			break
		}
		if line == "" {
			continue
		}
		if h := parseHgDiffHeaderItem(line); h != nil {
			item.Header = append(item.Header, h)
			continue
		}
		// An unknown line means there is nothing more we understand for this
		// file. Put it back; the outer loop will decide what to do with it.
		lr.unreadLine(raw)
		return item, nil
	}

	raw, err := lr.readLine()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	line := trimLineEnding(raw)
	if !strings.HasPrefix(line, "+++ ") {
		if raw != "" {
			lr.unreadLine(raw)
		}
		if sawFileHeader {
			item.File1 = file1
		}
		return item, nil
	}
	file2 = strings.TrimSpace(line[len("+++ "):])
	item.File1 = file1
	item.File2 = file2

	for {
		raw, err := lr.readLine()
		if errors.Is(err, io.EOF) && raw == "" {
			return item, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		line = trimLineEnding(raw)
		if line == "" {
			continue
		}
		m := reHgDiffHunk.FindStringSubmatch(line)
		if m == nil {
			lr.unreadLine(raw)
			return item, nil
		}
		patch, err := parseHgDiffHunk(lr, m)
		if err != nil {
			return nil, err
		}
		item.PatchList = append(item.PatchList, patch)
	}
}

func parseHgDiffHunk(lr *lineReader, m []string) (*DiffItemPatch, error) {
	lStart, _ := strconv.ParseInt(m[1], 10, 64)
	lCount := int64(1)
	if m[2] != "" {
		lCount, _ = strconv.ParseInt(m[2], 10, 64)
	}
	rStart, _ := strconv.ParseInt(m[3], 10, 64)
	rCount := int64(1)
	if m[4] != "" {
		rCount, _ = strconv.ParseInt(m[4], 10, 64)
	}
	patch := &DiffItemPatch{
		LStart:      lStart,
		LLineCount:  lCount,
		RStart:      rStart,
		RLineCount:  rCount,
		ContextLine: m[5],
		LineList:    make([]AnnotatedLine, 0),
	}
	f1 := lStart
	f2 := rStart
	for {
		raw, err := lr.readLine()
		if errors.Is(err, io.EOF) && raw == "" {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			break
		}
		if raw == "" {
			break
		}
		if raw[0] == '\\' {
			// "\ No newline at end of file" does not consume a line number.
			continue
		}
		if raw[0] != ' ' && raw[0] != '+' && raw[0] != '-' {
			lr.unreadLine(raw)
			break
		}
		content := raw[1:]
		switch raw[0] {
		case ' ':
			patch.LineList = append(patch.LineList, AnnotatedLine{
				Type: SAME, F1LineNum: f1, F2LineNum: f2, Line: content,
			})
			f1++
			f2++
		case '+':
			patch.LineList = append(patch.LineList, AnnotatedLine{
				Type: APPEND, F1LineNum: f1, F2LineNum: f2, Line: content,
			})
			f2++
		case '-':
			patch.LineList = append(patch.LineList, AnnotatedLine{
				Type: DELETE, F1LineNum: f1, F2LineNum: f2, Line: content,
			})
			f1++
		}
	}
	return patch, nil
}

// GetDiff returns the diff of a changeset against its first parent. It runs
// hg diff --git; Mercurial has no on-disk format for a precomputed diff.
func (r *LocalHgRepository) GetDiff(commitId string) (*Diff, error) {
	c, err := r.GetCommit(commitId)
	if err != nil {
		return nil, err
	}
	return r.GetDiffRev(c.Rev)
}

// GetDiffRev is GetDiff for a changelog revision number.
func (r *LocalHgRepository) GetDiffRev(rev int) (*Diff, error) {
	cmd := hgCommand("diff", "--git", "--nodates", "-c", strconv.Itoa(rev))
	cmd.Dir = r.RootPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("hg diff: %s: %s", err, strings.TrimSpace(stderr.String()))
	}
	diff, err := parseHgDiff(stdout.Bytes())
	if err != nil {
		return nil, err
	}
	if rev >= 0 && rev < r.Changelog.Len() {
		diff.CommitHash = r.Changelog.Entries[rev].Node
	}
	return diff, nil
}

// GetDiffBetween returns the diff between two revisions.
func (r *LocalHgRepository) GetDiffBetween(fromRev, toRev string) (*Diff, error) {
	cmd := hgCommand("diff", "--git", "--nodates", "-r", fromRev, "-r", toRev)
	cmd.Dir = r.RootPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("hg diff: %s: %s", err, strings.TrimSpace(stderr.String()))
	}
	diff, err := parseHgDiff(stdout.Bytes())
	if err != nil {
		return nil, err
	}
	if c, err := r.GetCommit(toRev); err == nil {
		diff.CommitHash = c.Id
	}
	return diff, nil
}
