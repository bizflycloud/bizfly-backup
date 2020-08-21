package backupapi

import (
	"fmt"
	"io"
	"strings"

	"github.com/dustin/go-humanize"
)

// NewProgressWriter returns new progress writer.
func NewProgressWriter(out io.Writer) *ProgressWriter {
	return &ProgressWriter{w: out}
}

// ProgressWriter wraps a writer, counts number of bytes written to it and write the report
// back to writer.
type ProgressWriter struct {
	w     io.Writer
	total uint64
}

// Write implements io.Writer interface.
//
// Report the progress to underlying writer before return.
func (pc *ProgressWriter) Write(buf []byte) (int, error) {
	defer pc.report()
	n := len(buf)
	pc.total += uint64(n)
	return n, nil
}

func (pc *ProgressWriter) report() {
	_, _ = fmt.Fprintf(pc.w, "\r%s", strings.Repeat(" ", 20))
	_, _ = fmt.Fprintf(pc.w, "\rTotal: %s done", humanize.Bytes(pc.total))
}
