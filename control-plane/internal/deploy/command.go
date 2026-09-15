package deploy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// streamCommand runs cmd, sending both of its output streams to logs one line
// at a time rather than buffering until it exits.
//
// Every driver shells out to a vendor CLI, and every one of them needs this
// same loop: a build that takes minutes has to show progress while it runs, and
// a build that fails has to have said why before it did. The caller adds the
// context to the returned error, because only it knows which command this was.
func streamCommand(cmd *exec.Cmd, logs LogWriter) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pipe stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("pipe stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// Both pipes must be drained concurrently: a command that fills one while
	// the reader is blocked on the other would deadlock.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); scan(stdout, "stdout", logs) }()
	go func() { defer wg.Done(); scan(stderr, "stderr", logs) }()
	wg.Wait()

	return cmd.Wait()
}

// maxLogLine is the longest line recorded in one piece. Build output can carry
// very long ones — a bundler's manifest, a base64 blob echoed by a RUN step —
// and anything past the cap continues on the following line.
const maxLogLine = 64 * 1024

// scan sends r to logs one line at a time.
//
// Cutting an over-long line into pieces is the whole reason this is not a
// bufio.Scanner, whose answer to a line longer than its buffer is to stop
// scanning: every line the command went on to emit would be dropped without a
// word, which is worst in exactly the case that produces enormous lines — a
// build that is about to fail and explain itself. Because nothing is lost now,
// the cap only decides how output is divided rather than how much of it
// survives, so it can be a buffer small enough to allocate per command.
func scan(r io.Reader, stream string, logs LogWriter) {
	reader := bufio.NewReaderSize(r, maxLogLine)

	for {
		line, err := reader.ReadSlice('\n')
		// ReadSlice returns what it read alongside the error, so a command whose
		// last line had no terminator still has that line recorded.
		if len(line) > 0 {
			logs.WriteLine(stream, string(trimEOL(line)))
		}
		// A line too long for the buffer comes back as ErrBufferFull, with the
		// remainder of it waiting for the next read.
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return
		}
	}
}

// trimEOL drops one line terminator, a CRLF included, exactly as bufio's own
// line splitter does. A bare carriage return is left alone: the vendor CLIs
// redraw progress with it, so it is content rather than a line ending.
func trimEOL(line []byte) []byte {
	return bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
}
