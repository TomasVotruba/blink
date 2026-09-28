package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"
)

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
	sortSlowestFirst(files, timings)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	workers := min(*workerCount, len(files))
	fmt.Fprintf(stdout, "blink - %d files, %d workers\n\n", len(files), workers)

	start := time.Now()
	report := newReport(stdout)

	for result := range runFiles(ctx, files, workers, *php, script, autoloadFile, phpunitArgs) {
		report.add(result)
		timings[result.File] = result.Duration.Seconds()
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

// runFiles runs files on a pool of workers. Every worker takes the next file from one shared queue
// once it is free, so a fast worker picks up more files.
func runFiles(ctx context.Context, files []string, workerCount int, php, script, autoloadFile string, phpunitArgs []string) <-chan fileResult {
	queue := make(chan string, len(files))
	for _, file := range files {
		queue <- file
	}
	close(queue)

	results := make(chan fileResult)
	var wg sync.WaitGroup

	for range workerCount {
		wg.Go(func() {
			var w *worker
			defer func() {
				if w != nil {
					w.stop()
				}
			}()

			for file := range queue {
				if ctx.Err() != nil {
					return
				}

				if w == nil {
					var err error
					w, err = startWorker(ctx, php, script, autoloadFile, phpunitArgs)
					if err != nil {
						results <- fileResult{File: file, Problem: "cannot start worker: " + err.Error()}
						continue
					}
				}

				start := time.Now()
				result, err := w.run(file)
				result.Duration = time.Since(start)
				if errors.Is(err, errWorkerDied) {
					// the next file gets a fresh worker
					w = nil
				}

				results <- result
			}
		})
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	return results
}
