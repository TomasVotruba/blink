package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"slices"
)

const timingsFile = ".blink-timings.json"

// loadTimings returns seconds per test file from the previous run.
func loadTimings(path string) (map[string]float64, error) {
	timings := map[string]float64{}

	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return timings, nil
	}
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(content, &timings); err != nil {
		return nil, err
	}

	return timings, nil
}

func saveTimings(path string, timings map[string]float64) error {
	content, err := json.MarshalIndent(timings, "", "    ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, append(content, '\n'), 0o644)
}

// sortSlowestFirst puts files without a known duration first, then the slowest ones,
// so the long files start early and the short ones fill the gaps at the end.
func sortSlowestFirst(files []string, timings map[string]float64) {
	slices.SortStableFunc(files, func(a, b string) int {
		durationA, knownA := timings[a]
		durationB, knownB := timings[b]

		switch {
		case knownA != knownB:
			if !knownA {
				return -1
			}
			return 1
		case durationA > durationB:
			return -1
		case durationA < durationB:
			return 1
		}

		return 0
	})
}
