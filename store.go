package hglib

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

const (
	storeMaxPathLen   = 120
	storeDirPrefixLen = 8
	storeMaxShortDirs = 8*(storeDirPrefixLen+1) - 4
)

// readRequires reads a Mercurial "requires" file: one feature name per line.
func readRequires(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var res []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		res = append(res, line)
	}
	return res, nil
}

func hasRequirement(list []string, name string) bool {
	for _, v := range list {
		if v == name {
			return true
		}
	}
	return false
}

// encodedir prevents a file named foo.i from colliding with a directory
// holding a revlog named foo. It is Mercurial's store.encodedir.
func encodedir(path string) string {
	path = strings.ReplaceAll(path, ".hg/", ".hg.hg/")
	path = strings.ReplaceAll(path, ".i/", ".i.hg/")
	path = strings.ReplaceAll(path, ".d/", ".d.hg/")
	return path
}

func isStoreReserved(c byte) bool {
	if c < 32 || c >= 126 {
		return true
	}
	switch c {
	case '\\', ':', '*', '?', '"', '<', '>', '|':
		return true
	}
	return false
}

// encodeStoreName is Mercurial's store._encodefname. It is reversible: upper
// case letters get an underscore prefix, the underscore itself is doubled,
// and reserved bytes become ~xx.
func encodeStoreName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte('_')
			b.WriteByte(c - 'A' + 'a')
		case c == '_':
			b.WriteString("__")
		case isStoreReserved(c):
			b.WriteByte('~')
			b.WriteByte(hexDigit(c >> 4))
			b.WriteByte(hexDigit(c & 0x0F))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// lowerEncode is Mercurial's store.lowerencode, used only by the hashed
// encoding. Unlike encodeStoreName it does not add an underscore before upper
// case letters.
func lowerEncode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c - 'A' + 'a')
		case isStoreReserved(c):
			b.WriteByte('~')
			b.WriteByte(hexDigit(c >> 4))
			b.WriteByte(hexDigit(c & 0x0F))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func hexDigit(c byte) byte {
	if c < 10 {
		return c + '0'
	}
	return c - 10 + 'a'
}

func isWinReserved3(s string) bool {
	switch s {
	case "aux", "con", "prn", "nul":
		return true
	}
	return false
}

func isWinReserved4(s string) bool {
	if len(s) < 4 {
		return false
	}
	if s[:3] != "com" && s[:3] != "lpt" {
		return false
	}
	return s[3] >= '1' && s[3] <= '9'
}

// auxEncode is Mercurial's store._auxencode. It handles names that Windows
// cannot represent and, when dotencode is enabled, names beginning with a dot
// or a space.
func auxEncode(parts []string, dotencode bool) []string {
	for i, n := range parts {
		if n == "" {
			continue
		}
		if dotencode && (n[0] == '.' || n[0] == ' ') {
			n = "~" + string([]byte{hexDigit(n[0] >> 4), hexDigit(n[0] & 0x0F)}) + n[1:]
		} else {
			l := strings.IndexByte(n, '.')
			if l == -1 {
				l = len(n)
			}
			if (l == 3 && isWinReserved3(n[:3])) || (l == 4 && isWinReserved4(n[:4])) {
				n = n[:2] + "~" + string([]byte{hexDigit(n[2] >> 4), hexDigit(n[2] & 0x0F)}) + n[3:]
			}
		}
		if len(n) > 0 && (n[len(n)-1] == '.' || n[len(n)-1] == ' ') {
			n = n[:len(n)-1] + "~" + string([]byte{hexDigit(n[len(n)-1] >> 4), hexDigit(n[len(n)-1] & 0x0F)})
		}
		parts[i] = n
	}
	return parts
}

func splitExt(name string) (string, string) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return name, ""
	}
	// Python's os.path.splitext ignores a dot that is part of a leading run
	// of dots (".gitignore" has no extension).
	if strings.Trim(name[:i], ".") == "" {
		return name, ""
	}
	return name[:i], name[i:]
}

// hashEncode is Mercurial's store._hashencode, used for store paths longer
// than 120 characters. The result is not reversible.
func hashEncode(path string, dotencode bool) string {
	sum := sha1.Sum([]byte(path))
	digest := hex.EncodeToString(sum[:])
	le := path
	if len(le) >= 5 {
		le = le[5:] // skip "data/" or "meta/"
	}
	le = lowerEncode(le)
	parts := auxEncode(strings.Split(le, "/"), dotencode)
	basename := parts[len(parts)-1]
	_, ext := splitExt(basename)

	sdirs := make([]string, 0, len(parts)-1)
	sdirslen := 0
	for _, p := range parts[:len(parts)-1] {
		d := p
		if len(d) > storeDirPrefixLen {
			d = d[:storeDirPrefixLen]
		}
		if len(d) > 0 && (d[len(d)-1] == '.' || d[len(d)-1] == ' ') {
			d = d[:len(d)-1] + "_"
		}
		var t int
		if sdirslen == 0 {
			t = len(d)
		} else {
			t = sdirslen + 1 + len(d)
			if t > storeMaxShortDirs {
				break
			}
		}
		sdirs = append(sdirs, d)
		sdirslen = t
	}
	dirs := strings.Join(sdirs, "/")
	if len(dirs) > 0 {
		dirs += "/"
	}
	res := "dh/" + dirs + digest + ext
	spaceLeft := storeMaxPathLen - len(res)
	if spaceLeft > 0 {
		filler := basename
		if len(filler) > spaceLeft {
			filler = filler[:spaceLeft]
		}
		res = "dh/" + dirs + filler + digest + ext
	}
	return res
}

// EncodeStorePath converts a logical path inside the store (for example
// "data/src/main.go.i") to the name it has on disk. dotencode is the
// repository's "dotencode" requirement, which is on by default.
func EncodeStorePath(path string, dotencode bool) string {
	path = encodedir(path)
	encoded := encodeStoreName(path)
	parts := auxEncode(strings.Split(encoded, "/"), dotencode)
	res := strings.Join(parts, "/")
	if len(res) > storeMaxPathLen {
		res = hashEncode(path, dotencode)
	}
	return res
}

// filelogLogicalPath returns the store path of the filelog for a
// repository-relative file.
func filelogLogicalPath(filePath string) string {
	filePath = strings.TrimPrefix(filePath, "/")
	return "data/" + filePath + ".i"
}

// localPath joins a slash-separated path to a directory using the host's
// separator.
func localPath(dir, p string) string {
	if p == "" {
		return dir
	}
	return filepath.Join(dir, filepath.FromSlash(p))
}
