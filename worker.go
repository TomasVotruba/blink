package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const outputMarker = "\x00blink-eof\x00\n"

type status int

const (
	passed status = iota
	failed
	skipped
)

type testResult struct {
	Name    string
	Status  status
	Message string
	Details string
}

type fileResult struct {
	File     string
	Tests    []testResult
	Duration time.Duration
	// Problem is set when the file failed outside of a test, e.g. crash, fatal error or broken config
	Problem string
	Output  string
}

// worker is one long-running "php worker.php" process that runs test files one by one.
type worker struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events *bufio.Scanner
	output chan string
}

func startWorker(ctx context.Context, php, script, autoloadFile string, phpunitArgs []string) (*worker, error) {
	eventsRead, eventsWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, php, append([]string{script, autoloadFile}, phpunitArgs...)...)
	cmd.ExtraFiles = []*os.File{eventsWrite} // fd 3 in the worker
	cmd.Stdout = outputWrite
	cmd.Stderr = outputWrite
	// own process group, so cancel also kills the forked child running the tests
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// the worker holds the write ends now
	eventsWrite.Close()
	outputWrite.Close()

	events := bufio.NewScanner(eventsRead)
	events.Buffer(make([]byte, 64*1024), 64*1024*1024)

	w := &worker{cmd: cmd, stdin: stdin, events: events, output: make(chan string, 1)}
	go w.readOutput(outputRead)

	// wait until the worker has booted, so its boot time is not counted to the first file
	for events.Scan() {
		if msg, ok := parseMessage(events.Text()); ok && msg.Kind == "blink" && msg.Name == "ready" {
			return w, nil
		}
	}

	return nil, fmt.Errorf("worker failed to boot: %s", strings.TrimSpace(w.drainOutput()))
}

// readOutput splits the worker stdout/stderr into one chunk per test file.
func (w *worker) readOutput(r io.Reader) {
	reader := bufio.NewReader(r)
	var buffer bytes.Buffer

	for {
		line, err := reader.ReadBytes('\n')
		buffer.Write(line)

		if bytes.HasSuffix(buffer.Bytes(), []byte(outputMarker)) {
			w.output <- string(bytes.TrimSuffix(buffer.Bytes(), []byte(outputMarker)))
			buffer.Reset()
		}

		if err != nil {
			if buffer.Len() > 0 {
				w.output <- buffer.String()
			}
			close(w.output)
			return
		}
	}
}

var errWorkerDied = errors.New("worker died")

// run sends one test file to the worker and collects its results.
// It returns errWorkerDied when the worker process is gone and has to be replaced.
func (w *worker) run(file string) (fileResult, error) {
	result := fileResult{File: file}

	if _, err := fmt.Fprintln(w.stdin, file); err != nil {
		result.Problem = "worker died before running the file"
		result.Output = w.drainOutput()
		return result, errWorkerDied
	}

	var current *testResult
	testCount := 0

	for w.events.Scan() {
		msg, ok := parseMessage(w.events.Text())
		if !ok {
			continue
		}

		switch msg.Name {
		case "testStarted":
			current = &testResult{Name: testName(msg), Status: passed}
		case "testFailed":
			if current != nil {
				current.Status = failed
				current.Message = msg.Attrs["message"]
				current.Details = msg.Attrs["details"]
			}
		case "testCount":
			testCount, _ = strconv.Atoi(msg.Attrs["count"])
		case "testIgnored":
			if current != nil {
				current.Status = skipped
				current.Message = msg.Attrs["message"]
				continue
			}

			// PHPUnit 10+ skips a whole class with one message, e.g. for a missing extension
			for range max(testCount-len(result.Tests), 1) {
				result.Tests = append(result.Tests, testResult{Name: msg.Attrs["name"], Status: skipped, Message: msg.Attrs["message"]})
			}
		case "testFinished":
			if current != nil {
				result.Tests = append(result.Tests, *current)
				current = nil
			}
		case "done":
			if msg.Kind != "blink" {
				continue
			}

			result.Output = <-w.output
			exitCode, _ := strconv.Atoi(msg.Attrs["exit"])
			signal, _ := strconv.Atoi(msg.Attrs["signal"])

			problem := ""
			switch {
			case signal != 0:
				problem = fmt.Sprintf("process killed by signal %d", signal)
			case exitCode != 0:
				problem = fmt.Sprintf("process exited with code %d", exitCode)
			}

			switch {
			case current != nil:
				// crashed in the middle of a test, blame that test
				current.Status = failed
				current.Message = problem
				current.Details = result.Output
				result.Tests = append(result.Tests, *current)
			case signal != 0:
				result.Problem = problem
			case exitCode != 0 && !result.hasFailures() && !noTestsExecuted(result):
				result.Problem = problem
			}

			return result, nil
		}
	}

	result.Problem = "worker died"
	result.Output = w.drainOutput()
	if current != nil {
		current.Status = failed
		current.Message = result.Problem
		current.Details = result.Output
		result.Tests = append(result.Tests, *current)
	}

	return result, errWorkerDied
}

func (w *worker) drainOutput() string {
	w.kill()

	var output string
	for chunk := range w.output {
		output += chunk
	}

	return output
}

func (w *worker) kill() {
	_ = syscall.Kill(-w.cmd.Process.Pid, syscall.SIGKILL)
	_ = w.cmd.Wait()
}

func (w *worker) stop() {
	w.stdin.Close()
	_ = w.cmd.Wait()
}

func (r fileResult) hasFailures() bool {
	for _, test := range r.Tests {
		if test.Status == failed {
			return true
		}
	}

	return false
}

// noTestsExecuted detects a file with no matching tests, e.g. with --filter; PHPUnit 12+ exits with 1 then
func noTestsExecuted(result fileResult) bool {
	return len(result.Tests) == 0 && strings.Contains(result.Output, "No tests executed!")
}
