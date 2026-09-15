package deploy

import (
	"slices"
	"strings"
	"testing"
)

// recorded returns the captured lines, so a test can assert on them one at a
// time rather than on one joined string.
func (c *collectLogs) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.lines)
}

func TestScanSplitsOutputIntoLines(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{
			name:   "lines are recorded in order",
			output: "step 1\nstep 2\nstep 3\n",
			want:   []string{"stdout: step 1", "stdout: step 2", "stdout: step 3"},
		},
		{
			// A CLI killed mid-write, or one that simply does not terminate its
			// last line, has still said something worth keeping.
			name:   "a final line with no terminator is kept",
			output: "step 1\nkilled",
			want:   []string{"stdout: step 1", "stdout: killed"},
		},
		{
			// Build output is read by people, and the blank lines are part of
			// how it is laid out.
			name:   "blank lines survive",
			output: "step 1\n\nstep 2\n",
			want:   []string{"stdout: step 1", "stdout: ", "stdout: step 2"},
		},
		{
			name:   "a CRLF terminator is not recorded as content",
			output: "step 1\r\nstep 2\r\n",
			want:   []string{"stdout: step 1", "stdout: step 2"},
		},
		{
			// The vendor CLIs overwrite a progress line with a bare carriage
			// return. Dropping it would join two states of the same line into
			// one unreadable one.
			name:   "a bare carriage return stays in the line",
			output: "pulling 40%\rpulling 80%\n",
			want:   []string{"stdout: pulling 40%\rpulling 80%"},
		},
		{
			name:   "a command that said nothing records nothing",
			output: "",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs collectLogs
			scan(strings.NewReader(tt.output), "stdout", &logs)

			if got := logs.recorded(); !slices.Equal(got, tt.want) {
				t.Errorf("\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// TestScanKeepsReadingPastAnOverLongLine covers the failure that decided how
// scan is written. A single line longer than the buffer must not cost the
// output that follows it, because that output is where a build says why it
// failed.
func TestScanKeepsReadingPastAnOverLongLine(t *testing.T) {
	// Two and a bit buffers' worth, so the line is cut more than once and the
	// last piece is a partial one.
	huge := strings.Repeat("x", 2*maxLogLine+128)

	var logs collectLogs
	scan(strings.NewReader(huge+"\nERROR: the build failed\n"), "stderr", &logs)

	got := logs.recorded()
	if len(got) == 0 {
		t.Fatal("scan recorded nothing")
	}

	if last := got[len(got)-1]; last != "stderr: ERROR: the build failed" {
		t.Errorf("last line = %q, want the line after the long one", last)
	}

	// The long line is divided, not truncated: putting the pieces back together
	// has to reproduce it exactly.
	var rejoined strings.Builder
	for _, line := range got[:len(got)-1] {
		piece := strings.TrimPrefix(line, "stderr: ")
		if len(piece) > maxLogLine {
			t.Errorf("recorded a %d byte line, want at most %d", len(piece), maxLogLine)
		}
		rejoined.WriteString(piece)
	}
	if rejoined.String() != huge {
		t.Errorf("the pieces rejoined to %d bytes, want the %d that were written",
			rejoined.Len(), len(huge))
	}
}
