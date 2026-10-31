package hglib

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HGRC is a parsed Mercurial configuration file (hgrc). Section and key
// names are stored in lower case, because Mercurial looks them up without
// regard to case.
type HGRC struct {
	Path     string
	Sections map[string]map[string]string
	order    []string
}

func newHGRC() *HGRC {
	return &HGRC{
		Sections: make(map[string]map[string]string),
	}
}

// ReadHGRC reads path and the files it %include's. A missing file is not an
// error; it yields an empty configuration, which is what a repository without
// an hgrc has.
func ReadHGRC(path string) (*HGRC, error) {
	c := newHGRC()
	c.Path = path
	err := c.readFile(path, 0)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return c, nil
}

// ParseHGRC parses configuration text. Includes are resolved relative to
// baseDir; pass "" to disable include resolution.
func ParseHGRC(r io.Reader, baseDir string) (*HGRC, error) {
	c := newHGRC()
	err := c.parse(r, baseDir, 0)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *HGRC) readFile(path string, depth int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return c.parse(f, filepath.Dir(path), depth)
}

func (c *HGRC) parse(r io.Reader, baseDir string, depth int) error {
	if depth > 16 {
		return fmt.Errorf("hgrc include nesting is too deep")
	}
	sc := bufio.NewScanner(r)
	// hgrc values can be long, e.g. a hook command line with many arguments.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	section := ""
	var pending string
	flush := func(line string) error {
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			return nil
		}
		if strings.HasPrefix(trimmed, "%include ") {
			inc := strings.TrimSpace(trimmed[len("%include "):])
			if inc == "" || baseDir == "" {
				return nil
			}
			if !filepath.IsAbs(inc) {
				inc = filepath.Join(baseDir, inc)
			}
			return c.readFile(inc, depth+1)
		}
		if strings.HasPrefix(trimmed, "%unset ") {
			rest := strings.TrimSpace(trimmed[len("%unset "):])
			sec, key, ok := strings.Cut(rest, ".")
			if ok {
				if m, ok := c.Sections[strings.ToLower(strings.TrimSpace(sec))]; ok {
					delete(m, strings.ToLower(strings.TrimSpace(key)))
				}
			}
			return nil
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			if _, ok := c.Sections[section]; !ok {
				c.Sections[section] = make(map[string]string)
				c.order = append(c.order, section)
			}
			return nil
		}
		if section == "" {
			return nil
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" {
			return nil
		}
		m, ok := c.Sections[section]
		if !ok {
			m = make(map[string]string)
			c.Sections[section] = m
			c.order = append(c.order, section)
		}
		m[key] = value
		return nil
	}

	for sc.Scan() {
		line := sc.Text()
		if pending != "" {
			line = pending + line
			pending = ""
		}
		if strings.HasSuffix(line, "\\") {
			pending = strings.TrimSuffix(line, "\\")
			continue
		}
		if err := flush(line); err != nil {
			return err
		}
	}
	if pending != "" {
		if err := flush(pending); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Get returns the value of section.key.
func (c *HGRC) Get(section, key string) (string, bool) {
	if c == nil {
		return "", false
	}
	m, ok := c.Sections[strings.ToLower(section)]
	if !ok {
		return "", false
	}
	v, ok := m[strings.ToLower(key)]
	return v, ok
}

// GetDefault returns the value of section.key, or def when it is absent.
func (c *HGRC) GetDefault(section, key, def string) string {
	if v, ok := c.Get(section, key); ok {
		return v
	}
	return def
}

// GetSection returns a copy of a whole section.
func (c *HGRC) GetSection(section string) (map[string]string, bool) {
	if c == nil {
		return nil, false
	}
	m, ok := c.Sections[strings.ToLower(section)]
	if !ok {
		return nil, false
	}
	res := make(map[string]string, len(m))
	for k, v := range m {
		res[k] = v
	}
	return res, true
}

// GetBool interprets a configuration value as a boolean. Mercurial accepts
// "1", "yes", "true", "on" (and their negatives); its default when a value is
// absent depends on the option, so def is used.
func (c *HGRC) GetBool(section, key string, def bool) bool {
	v, ok := c.Get(section, key)
	if !ok {
		return def
	}
	return parseHgBool(v, def)
}

func parseHgBool(v string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "yes", "true", "on", "always":
		return true
	case "0", "no", "false", "off", "never":
		return false
	default:
		return def
	}
}

// GetInt interprets a configuration value as an integer.
func (c *HGRC) GetInt(section, key string, def int) int {
	v, ok := c.Get(section, key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

// GetList splits a configuration value on commas and whitespace, which is how
// Mercurial treats list-valued options.
func (c *HGRC) GetList(section, key string) []string {
	v, ok := c.Get(section, key)
	if !ok {
		return nil
	}
	return splitConfigList(v)
}

func splitConfigList(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	res := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			res = append(res, f)
		}
	}
	return res
}

// SectionNames returns the sections in the order they first appeared.
func (c *HGRC) SectionNames() []string {
	if c == nil {
		return nil
	}
	res := make([]string, len(c.order))
	copy(res, c.order)
	return res
}

// Set changes a value in memory. It does not write the file back.
func (c *HGRC) Set(section, key, value string) {
	if c.Sections == nil {
		c.Sections = make(map[string]map[string]string)
	}
	section = strings.ToLower(section)
	key = strings.ToLower(key)
	m, ok := c.Sections[section]
	if !ok {
		m = make(map[string]string)
		c.Sections[section] = m
		c.order = append(c.order, section)
	}
	m[key] = value
}
