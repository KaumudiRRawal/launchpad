package deploy

import (
	"bufio"
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

func scan(r io.Reader, stream string, logs LogWriter) {
	scanner := bufio.NewScanner(r)
	// Build output can carry very long lines; the default 64 KiB limit would
	// abort the scan and silently truncate the log.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		logs.WriteLine(stream, scanner.Text())
	}
}
