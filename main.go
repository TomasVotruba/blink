package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"
)

// more chunks than workers lets a free worker pick up remaining work, fewer saves on PHPUnit and app boots
const chunksPerWorker = 3

//go:embed worker.php
var workerScript []byte

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("blink", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: blink [options] [paths...] [-- phpunit options]")
		flags.PrintDefaults()
	}
	workerCount := flags.Int("j", runtime.NumCPU(), "number of parallel workers")
	php := flags.String("php", "php", "PHP binary")
	configOption := flags.String("c", "", "PHPUnit configuration file (default: phpunit.xml or phpunit.xml.dist)")

	// split before parsing, flag.Parse drops the "--" itself
	args, phpunitArgs := splitArgs(args)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	paths := flags.Args()

	configPath, err := findConfig(*configOption)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if configPath != "" {
		phpunitArgs = append([]string{"--configuration", configPath}, phpunitArgs...)
	}

	var files []string
	switch {
	case len(paths) > 0:
		files, err = discoverFromPaths(paths)
	case configPath != "":
		files, err = discoverFromConfig(configPath)
	default:
		err = errors.New("no paths given and no phpunit.xml or phpunit.xml.dist found")
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(files) == 0 {
		fmt.Fprintln(stderr, "no test files found")
		return 2
	}

	autoloadFile, err := filepath.Abs("vendor/autoload.php")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if _, err := os.Stat(autoloadFile); err != nil {
		fmt.Fprintln(stderr, "vendor/autoload.php not found, run composer install first")
		return 2
	}

	scriptDir, err := os.MkdirTemp("", "blink")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	defer os.RemoveAll(scriptDir)

	script := filepath.Join(scriptDir, "worker.php")
	if err := os.WriteFile(script, workerScript, 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	timings, err := loadTimings(timingsFile)
	if err != nil {
		fmt.Fprintf(stderr, "ignoring %s: %v\n", timingsFile, err)
		timings = map[string]float64{}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	workers := min(*workerCount, len(files))
	chunks := chunkFiles(files, timings, workers*chunksPerWorker)
	fmt.Fprintf(stdout, "blink - %d files, %d workers\n\n", len(files), workers)

	start := time.Now()
	report := newReport(stdout)

	for result := range runChunks(ctx, chunks, workers, *php, script, autoloadFile, phpunitArgs) {
		report.add(&result)
		maps.Copy(timings, result.Durations)
	}

	if ctx.Err() != nil {
		fmt.Fprintln(stderr, "\ninterrupted")
		return 130
	}

	if err := saveTimings(timingsFile, timings); err != nil {
		fmt.Fprintf(stderr, "cannot save %s: %v\n", timingsFile, err)
	}

	return report.finish(time.Since(start))
}

// splitArgs separates paths from PHPUnit options given after "--".
func splitArgs(args []string) ([]string, []string) {
	index := slices.Index(args, "--")
	if index == -1 {
		return args, nil
	}

	return args[:index], args[index+1:]
}

// runChunks runs chunks of files on a pool of workers. Every worker takes the next chunk from one shared queue
// once it is free, so a fast worker picks up more chunks.
func runChunks(ctx context.Context, chunks [][]string, workerCount int, php, script, autoloadFile string, phpunitArgs []string) <-chan runResult {
	queue := make(chan []string, len(chunks))
	for _, chunk := range chunks {
		queue <- chunk
	}
	close(queue)

	results := make(chan runResult)
	var wg sync.WaitGroup

	for range workerCount {
		wg.Go(func() {
			var w *worker
			defer func() {
				if w != nil {
					w.stop()
				}
			}()

			for chunk := range queue {
				pending := [][]string{chunk}

				for len(pending) > 0 {
					if ctx.Err() != nil {
						return
					}

					files := pending[0]
					pending = pending[1:]

					if w == nil {
						var err error
						w, err = startWorker(ctx, php, script, autoloadFile, phpunitArgs)
						if err != nil {
							results <- runResult{Files: files, Problem: "cannot start worker: " + err.Error()}
							continue
						}
					}

					result, err := w.run(files)
					if errors.Is(err, errWorkerDied) {
						// the next run gets a fresh worker
						w = nil
					}
					reruns := rerunFiles(&result)
					if len(reruns) > 0 && len(result.notStarted()) == len(files) {
						// nothing ran, the reruns report the problem
						result.Problem, result.Output = "", ""
					}

					results <- result
					pending = append(pending, reruns...)
				}
			}
		})
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	return results
}

// rerunFiles returns files that did not run because their run ended early.
// Without any progress, e.g. on a syntax error, each file runs alone to find the broken one.
func rerunFiles(result *runResult) [][]string {
	if !result.Crashed && result.Problem == "" {
		return nil
	}

	notStarted := result.notStarted()
	switch {
	case len(notStarted) == 0:
		return nil
	case len(notStarted) < len(result.Files):
		return [][]string{notStarted}
	case len(notStarted) > 1:
		var runs [][]string
		for _, file := range notStarted {
			runs = append(runs, []string{file})
		}
		return runs
	}

	return nil
}
