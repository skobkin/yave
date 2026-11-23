package ffprobe

import (
	"bytes"
	"os/exec"
	"strings"
)

// runProbe runs cmd and returns its stdout alone. ffprobe prints warnings
// and decode errors on stderr even when it exits successfully, so mixing
// the streams would corrupt the JSON document. stderr is returned
// separately, trimmed, for error context.
func runProbe(cmd *exec.Cmd) (stdout []byte, stderr string, err error) {
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	stdout, err = cmd.Output()
	return stdout, strings.TrimSpace(errBuf.String()), err
}
