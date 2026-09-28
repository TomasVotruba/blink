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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	File    string
	Status  status
	Message string
	Details string
}

// runResult is the outcome of one batch of test files run in a single PHPUnit process.
type runResult struct {
	Files []string
	Tests []testResult
	// Started holds files that have at least one test started, the rest did not run, e.g. after a crash
	Started map[string]bool
	// Durations holds summed test durations in seconds per file
	Durations map[string]float64
	// Problem is set when the run failed outside of a test, e.g. crash, fatal error or broken config
	Problem string
	Output  string
	// Crashed is set when the process ended early, so some files may not have run
	Crashed bool
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

		if before, ok := bytes.CutSuffix(buffer.Bytes(), []byte(outputMarker)); ok {
			w.output <- string(before)
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

// run sends test files to the worker, which runs them in one PHPUnit process, and collects the results.
// It returns errWorkerDied when the worker process is gone and has to be replaced.
func (w *worker) run(files []string) (runResult, error) {
	result := runResult{Files: files, Started: map[string]bool{}, Durations: map[string]float64{}}

	// PHPUnit reports real absolute paths
	fileByPath := map[string]string{}
	for _, file := range files {
		fileByPath[realPath(file)] = file
	}

	if _, err := fmt.Fprintln(w.stdin, strings.Join(files, "\t")); err != nil {
		result.Problem = "worker died before running the files"
		result.Crashed = true
		result.Output = w.drainOutput()
		return result, errWorkerDied
	}

	var current *testResult

	// a test that never finished crashed its process, blame it
	crashed := func(message, output string) {
		result.Crashed = true
		current.Status = failed
		current.Message = message
		current.Details = output
		result.Tests = append(result.Tests, *current)
		current = nil
	}

	for w.events.Scan() {
		msg, ok := parseMessage(w.events.Text())
		if !ok {
			continue
		}

		switch msg.Name {
		case "testStarted":
			if current != nil {
				// PHPUnit 9 runs each file in its own process, so the next file starts after a crash
				crashed("process crashed during test", "")
			}

			file := fileByPath[testPath(msg)]
			result.Started[file] = true
			current = &testResult{Name: testName(msg), File: file, Status: passed}
		case "testFailed":
			if current != nil {
				current.Status = failed
				current.Message = msg.Attrs["message"]
				current.Details = msg.Attrs["details"]
			}
		case "testIgnored":
			// a skipped class comes as "classSkipped" with the number of its tests
			if current != nil {
				current.Status = skipped
				current.Message = msg.Attrs["message"]
			}
		case "testFinished":
			if current != nil {
				milliseconds, _ := strconv.ParseFloat(msg.Attrs["duration"], 64)
				result.Durations[current.File] += milliseconds / 1000
				result.Tests = append(result.Tests, *current)
				current = nil
			}
		case "classSkipped":
			count, _ := strconv.Atoi(msg.Attrs["count"])
			for range count {
				result.Tests = append(result.Tests, testResult{Name: msg.Attrs["name"], Status: skipped, Message: msg.Attrs["message"]})
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
				crashed(problem, result.Output)
			case signal != 0:
				result.Problem = problem
				result.Crashed = true
			case exitCode != 0 && !result.hasFailures() && !noTestsExecuted(&result):
				result.Problem = problem
			}

			return result, nil
		}
	}

	result.Problem = "worker died"
	result.Crashed = true
	result.Output = w.drainOutput()
	if current != nil {
		crashed(result.Problem, result.Output)
	}

	return result, errWorkerDied
}

func (w *worker) drainOutput() string {
	w.kill()

	var output strings.Builder
	for chunk := range w.output {
		output.WriteString(chunk)
	}

	return output.String()
}

func (w *worker) kill() {
	_ = syscall.Kill(-w.cmd.Process.Pid, syscall.SIGKILL)
	_ = w.cmd.Wait()
}

func (w *worker) stop() {
	w.stdin.Close()
	_ = w.cmd.Wait()
}

func (r *runResult) hasFailures() bool {
	for _, test := range r.Tests {
		if test.Status == failed {
			return true
		}
	}

	return false
}

// noTestsExecuted detects files with no matching tests, e.g. with --filter; PHPUnit 12+ exits with 1 then
func noTestsExecuted(result *runResult) bool {
	return len(result.Tests) == 0 && strings.Contains(result.Output, "No tests executed!")
}

// notStarted returns files of the run that have no started test
func (r *runResult) notStarted() []string {
	var files []string
	for _, file := range r.Files {
		if !r.Started[file] {
			files = append(files, file)
		}
	}

	return files
}

func realPath(file string) string {
	path, err := filepath.Abs(file)
	if err != nil {
		return file
	}

	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}

	return path
}
