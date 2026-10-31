package hglib

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SetConfigValue changes one key of an hgrc file without disturbing the rest
// of the file. Comments and unrelated sections are preserved. The file is
// replaced atomically so that an interrupted write cannot leave half an hgrc.
func SetConfigValue(path, section, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(data), "\n")
	secLower := strings.ToLower(section)
	keyLower := strings.ToLower(key)

	secStart := -1
	secEnd := len(lines)
	for i, line := range lines {
		t := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			if secStart >= 0 {
				secEnd = i
				break
			}
			name := strings.ToLower(strings.TrimSpace(t[1 : len(t)-1]))
			if name == secLower {
				secStart = i
			}
		}
	}
	if secStart >= 0 && secEnd < secStart {
		secEnd = len(lines)
	}

	if secStart < 0 {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "["+section+"]", key+" = "+value)
	} else {
		found := false
		for i := secStart + 1; i < secEnd; i++ {
			t := strings.TrimSpace(strings.TrimSuffix(lines[i], "\r"))
			if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
				continue
			}
			k, _, ok := strings.Cut(t, "=")
			if ok && strings.ToLower(strings.TrimSpace(k)) == keyLower {
				lines[i] = key + " = " + value
				found = true
				break
			}
		}
		if !found {
			insertAt := secEnd
			for insertAt > secStart+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
				insertAt--
			}
			lines = append(lines, "")
			copy(lines[insertAt+1:], lines[insertAt:])
			lines[insertAt] = key + " = " + value
		}
	}
	return writeFileAtomic(path, []byte(strings.Join(lines, "\n")))
}

// UnsetConfigValue removes one key from an hgrc section.
func UnsetConfigValue(path, section, key string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(data), "\n")
	secLower := strings.ToLower(section)
	keyLower := strings.ToLower(key)
	inSection := false
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		t := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			inSection = strings.ToLower(strings.TrimSpace(t[1:len(t)-1])) == secLower
			out = append(out, line)
			continue
		}
		if inSection {
			k, _, ok := strings.Cut(t, "=")
			if ok && strings.ToLower(strings.TrimSpace(k)) == keyLower {
				continue
			}
		}
		out = append(out, line)
	}
	return writeFileAtomic(path, []byte(strings.Join(out, "\n")))
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".hgrc-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SyncLocalDescription writes r.Description to the [web] description of the
// repository's hgrc.
func (r *LocalHgRepository) SyncLocalDescription() error {
	path := filepath.Join(r.HgDirectoryPath, "hgrc")
	if err := SetConfigValue(path, "web", "description", r.Description); err != nil {
		return err
	}
	cfg, err := ReadHGRC(path)
	if err != nil {
		return err
	}
	r.Config = cfg
	return nil
}

// ReloadConfig re-reads .hg/hgrc and refreshes the cached Description.
func (r *LocalHgRepository) ReloadConfig() error {
	cfg, err := ReadHGRC(filepath.Join(r.HgDirectoryPath, "hgrc"))
	if err != nil {
		return err
	}
	r.Config = cfg
	r.Description = cfg.GetDefault("web", "description", r.Description)
	r.DefaultUser = cfg.GetDefault("ui", "username", r.DefaultUser)
	return nil
}

func hgrcPathFor(repo *LocalHgRepository) (string, error) {
	if repo == nil || repo.HgDirectoryPath == "" {
		return "", fmt.Errorf("repository has no hg directory")
	}
	return filepath.Join(repo.HgDirectoryPath, "hgrc"), nil
}
