package hglib

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// AuthorTime is one side of a Mercurial changeset's user name and date.
// Mercurial records a single user per changeset; AuthorInfo and CommitterInfo
// are therefore the same value. The field names are kept close to gitlib's.
type AuthorTime struct {
	AuthorName  string
	AuthorEmail string
	Time        time.Time
}

func (at *AuthorTime) String() string {
	if at == nil {
		return ""
	}
	if at.AuthorName == "" {
		return fmt.Sprintf("<%s>", at.AuthorEmail)
	}
	return fmt.Sprintf("%s <%s>", at.AuthorName, at.AuthorEmail)
}

// CommitObject is a Mercurial changeset. TreeObjId is the manifest node, which
// plays the role Git's tree object plays for a commit. ParentIdList contains
// the first parent first, as Mercurial does.
type CommitObject struct {
	Id            string
	Rev           int
	TreeObjId     string
	ParentIdList  []string
	P1            int
	P2            int
	AuthorInfo    AuthorTime
	CommitterInfo AuthorTime
	CoAuthorInfo  []AuthorTime
	CommitTime    time.Time
	CommitMessage string
	Branch        string
	Files         []string
	Extra         map[string]string
	Signature     string
	rawData       []byte
}

func (c *CommitObject) ParentId() string {
	if c == nil || len(c.ParentIdList) == 0 {
		return ""
	}
	return c.ParentIdList[0]
}

func (c *CommitObject) IsMerge() bool {
	return c != nil && c.P2 >= 0
}

func (c *CommitObject) Type() HgObjectType { return CHANGESET }
func (c *CommitObject) ObjectId() string   { return c.Id }
func (c *CommitObject) RawData() []byte    { return c.rawData }

func (c *CommitObject) String() string {
	return fmt.Sprintf("Commit{%s,%s}", c.Id, c.TreeObjId)
}

var reCoAuthoredBy = regexp.MustCompile(`^\s*[cC]o-[aA]uthored-[bB]y:\s*([^<>]+)\s*<([^>]*)>\s*$`)

func parseUser(s string) AuthorTime {
	s = strings.TrimSpace(s)
	res := AuthorTime{}
	if i := strings.LastIndexByte(s, '<'); i >= 0 && strings.HasSuffix(s, ">") {
		res.AuthorName = strings.TrimSpace(s[:i])
		res.AuthorEmail = strings.TrimSpace(s[i+1 : len(s)-1])
		return res
	}
	// A bare address is common in old Mercurial history.
	res.AuthorName = s
	return res
}

// parseHgDate parses Mercurial's "<unix time> <seconds west of UTC>" pair.
// Mercurial stores timezone offsets with the opposite sign of Go's time
// package, so the value is negated here.
func parseHgDate(timeStr, tzStr string) time.Time {
	f, err := strconv.ParseFloat(timeStr, 64)
	if err != nil {
		return time.Unix(0, 0).UTC()
	}
	sec := int64(f)
	nsec := int64((f - float64(sec)) * 1e9)
	tz, err := strconv.Atoi(tzStr)
	if err != nil {
		tz = 0
	}
	return time.Unix(sec, nsec).In(time.FixedZone("", -tz))
}

func unescapeHgString(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case '0':
			b.WriteByte(0)
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// decodeExtra decodes the extra metadata field of a changelog entry. The
// result always has a "branch" key, defaulting to "default", like Mercurial.
func decodeExtra(s string) map[string]string {
	res := make(map[string]string)
	for _, item := range strings.Split(s, "\x00") {
		if item == "" {
			continue
		}
		k, v, ok := strings.Cut(item, ":")
		if !ok {
			continue
		}
		res[unescapeHgString(k)] = unescapeHgString(v)
	}
	if _, ok := res["branch"]; !ok {
		res["branch"] = "default"
	}
	return res
}

func encodeExtra(m map[string]string) string {
	// Keys are sorted so that the result is deterministic.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, escapeHgString(k)+":"+escapeHgString(m[k]))
	}
	return strings.Join(parts, "\x00")
}

func escapeHgString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\x00", "\\0")
	return s
}

func sortStrings(s []string) {
	// A tiny insertion sort keeps this file dependency-free and the lists are
	// short (the number of extra keys).
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// parseCommitObject parses the text of one changelog revision. The format is
//
//	<manifest node>\n
//	<user>\n
//	<time> <timezone> <extra>\n
//	<file>\n ... \n
//	\n
//	<description>
func parseCommitObject(node string, rev int, text []byte) (*CommitObject, error) {
	nl1 := bytes.IndexByte(text, '\n')
	if nl1 < 0 {
		return nil, fmt.Errorf("changeset %s: missing header newline", node)
	}
	nl2rel := bytes.IndexByte(text[nl1+1:], '\n')
	if nl2rel < 0 {
		return nil, fmt.Errorf("changeset %s: missing user newline", node)
	}
	nl2 := nl1 + 1 + nl2rel
	nl3rel := bytes.IndexByte(text[nl2+1:], '\n')
	if nl3rel < 0 {
		return nil, fmt.Errorf("changeset %s: missing date newline", node)
	}
	nl3 := nl2 + 1 + nl3rel

	res := &CommitObject{
		Id:           node,
		Rev:          rev,
		TreeObjId:    string(text[:nl1]),
		AuthorInfo:   parseUser(string(text[nl1+1 : nl2])),
		Extra:        make(map[string]string),
		rawData:      text,
		ParentIdList: make([]string, 0, 2),
	}
	res.CommitterInfo = res.AuthorInfo

	dateLine := string(text[nl2+1 : nl3])
	dateFields := strings.SplitN(dateLine, " ", 3)
	if len(dateFields) >= 1 {
		tz := "0"
		if len(dateFields) >= 2 {
			tz = dateFields[1]
		}
		res.AuthorInfo.Time = parseHgDate(dateFields[0], tz)
		res.CommitterInfo.Time = res.AuthorInfo.Time
		res.CommitTime = res.AuthorInfo.Time
	}
	if len(dateFields) >= 3 {
		res.Extra = decodeExtra(dateFields[2])
	} else {
		res.Extra = decodeExtra("")
	}
	res.Branch = res.Extra["branch"]
	if res.Branch == "" {
		res.Branch = "default"
	}
	if v, ok := res.Extra["gpg-signature"]; ok {
		res.Signature = v
	}

	var filesPart []byte
	var descStart int
	if nl3+1 >= len(text) {
		descStart = len(text)
	} else if text[nl3+1] == '\n' {
		// No files changed.
		descStart = nl3 + 2
	} else {
		rel := bytes.Index(text[nl3+1:], []byte("\n\n"))
		if rel < 0 {
			filesPart = text[nl3+1:]
			descStart = len(text)
		} else {
			doubleNL := nl3 + 1 + rel
			filesPart = text[nl3+1 : doubleNL]
			descStart = doubleNL + 2
		}
	}
	if len(filesPart) > 0 {
		for _, line := range splitLines(filesPart) {
			if len(line) > 0 {
				res.Files = append(res.Files, string(line))
			}
		}
	}
	if descStart <= len(text) {
		res.CommitMessage = string(text[descStart:])
	}

	for _, line := range strings.Split(res.CommitMessage, "\n") {
		m := reCoAuthoredBy.FindStringSubmatch(strings.TrimSpace(line))
		if len(m) == 0 {
			continue
		}
		name := strings.TrimSpace(m[1])
		email := strings.TrimSpace(m[2])
		if authorTimeExists(res.CoAuthorInfo, name, email) {
			continue
		}
		res.CoAuthorInfo = append(res.CoAuthorInfo, AuthorTime{
			AuthorName:  name,
			AuthorEmail: email,
			Time:        res.AuthorInfo.Time,
		})
	}
	return res, nil
}

func authorTimeExists(list []AuthorTime, name, email string) bool {
	for _, v := range list {
		if v.AuthorName == name && v.AuthorEmail == email {
			return true
		}
	}
	return false
}
