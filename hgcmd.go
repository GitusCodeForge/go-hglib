package hglib

import (
	"os/exec"
)

// hgCommand builds an hg invocation. The extra config lets repositories that
// use fileindex-v1 be read on Mercurial builds without the Rust file-index
// extension; it is ignored by repositories and builds that do not need it.
func hgCommand(args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+2)
	full = append(full, "--config", "storage.fileindex.slow-path=allow")
	full = append(full, args...)
	cmd := exec.Command("hg", full...)
	cmd.Env = hgEnv()
	return cmd
}
