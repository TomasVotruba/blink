package main

import (
	"cmp"
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

// chunkFiles splits files into about count chunks of similar total duration, slowest chunk first.
// Neighbour files stay together, as they often share fixtures and warm caches.
// Files without a known duration count as an average one.
func chunkFiles(files []string, timings map[string]float64, count int) [][]string {
	known, sum := 0, 0.0
	for _, file := range files {
		if duration, ok := timings[file]; ok {
			known++
			sum += duration
		}
	}

	average := 1.0
	if known > 0 && sum > 0 {
		average = sum / float64(known)
	}

	weight := func(file string) float64 {
		if duration, ok := timings[file]; ok {
			return duration
		}
		return average
	}

	total := 0.0
	for _, file := range files {
		total += weight(file)
	}
	target := total / float64(count)

	var chunks [][]string
	var weights []float64
	var chunk []string //nolint:prealloc // starts empty for every chunk
	chunkWeight := 0.0

	for _, file := range files {
		// a heavy file starts its own chunk instead of overfilling the current one
		if len(chunk) > 0 && chunkWeight+weight(file) > target {
			chunks = append(chunks, chunk)
			weights = append(weights, chunkWeight)
			chunk, chunkWeight = nil, 0
		}

		chunk = append(chunk, file)
		chunkWeight += weight(file)

		if chunkWeight >= target {
			chunks = append(chunks, chunk)
			weights = append(weights, chunkWeight)
			chunk, chunkWeight = nil, 0
		}
	}
	if len(chunk) > 0 {
		chunks = append(chunks, chunk)
		weights = append(weights, chunkWeight)
	}

	indexes := make([]int, len(chunks))
	for i := range indexes {
		indexes[i] = i
	}
	slices.SortStableFunc(indexes, func(a, b int) int {
		return cmp.Compare(weights[b], weights[a])
	})

	sorted := make([][]string, len(chunks))
	for i, index := range indexes {
		sorted[i] = chunks[index]
	}

	return sorted
}
