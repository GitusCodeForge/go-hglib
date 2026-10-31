package hglib

import "bytes"

// lineReader reads lines and can push one line back. bufio.Reader's
// UnreadByte cannot unread a whole line, which the diff and annotate parsers
// need while looking one line ahead.
type lineReader struct {
	r      *bytes.Buffer
	buffer []string
}

func newLineReader(r *bytes.Buffer) *lineReader {
	return &lineReader{r: r}
}

func (r *lineReader) readLine() (string, error) {
	if n := len(r.buffer); n > 0 {
		line := r.buffer[n-1]
		r.buffer = r.buffer[:n-1]
		return line, nil
	}
	return r.r.ReadString('\n')
}

func (r *lineReader) unreadLine(line string) {
	r.buffer = append(r.buffer, line)
}

func trimLineEnding(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '\r' {
		s = s[:len(s)-1]
	}
	return s
}
