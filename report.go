package main

import (
	"fmt"
	"io"
	"strings"
	"time"
)

const lineWidth = 80

// report prints one character per test while files finish, then details of failures.
type report struct {
	out      io.Writer
	column   int
	failures []string
	problems []string
	counts   map[status]int
}

func newReport(out io.Writer) *report {
	return &report{out: out, counts: map[status]int{}}
}

func (r *report) add(result runResult) {
	for _, test := range result.Tests {
		r.counts[test.Status]++

		switch test.Status {
		case passed:
			r.progress(".")
		case failed:
			r.progress("F")
			r.failures = append(r.failures, formatFailure(test))
		case skipped:
			r.progress("S")
		}
	}

	if result.Problem != "" {
		r.progress("E")
		r.problems = append(r.problems, formatProblem(result))
	}
}

func (r *report) progress(char string) {
	if r.column == lineWidth {
		fmt.Fprintln(r.out)
		r.column = 0
	}

	fmt.Fprint(r.out, char)
	r.column++
}

// finish prints failures and the summary, and returns the exit code.
func (r *report) finish(elapsed time.Duration) int {
	fmt.Fprint(r.out, "\n\n")

	for i, failure := range r.failures {
		fmt.Fprintf(r.out, "%d) %s\n", i+1, failure)
	}

	for i, problem := range r.problems {
		fmt.Fprintf(r.out, "%d) %s\n", len(r.failures)+i+1, problem)
	}

	total := r.counts[passed] + r.counts[failed] + r.counts[skipped]
	fmt.Fprintf(r.out, "Tests: %d, Passed: %d, Failed: %d, Skipped: %d, Broken runs: %d - %.2fs\n",
		total, r.counts[passed], r.counts[failed], r.counts[skipped], len(r.problems), elapsed.Seconds())

	if r.counts[failed] > 0 || len(r.problems) > 0 {
		return 1
	}

	return 0
}

func formatFailure(test testResult) string {
	text := test.Name + "\n" + test.Message + "\n"
	if details := strings.TrimSpace(test.Details); details != "" {
		text += "\n" + details + "\n"
	}

	return text
}

func formatProblem(result runResult) string {
	files := result.Files[0]
	if len(result.Files) > 1 {
		files += fmt.Sprintf(" and %d more files", len(result.Files)-1)
	}

	text := files + ": " + result.Problem + "\n"
	if output := strings.TrimSpace(result.Output); output != "" {
		text += "\n" + output + "\n"
	}

	return text
}
