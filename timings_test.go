package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestChunkFiles(t *testing.T) {
	files := []string{"a.php", "b.php", "c.php", "d.php", "e.php"}
	timings := map[string]float64{"a.php": 1, "b.php": 1, "c.php": 4, "d.php": 1}

	// e.php is unknown, so it counts as the average of 1.75
	chunks := chunkFiles(files, timings, 3)

	expected := [][]string{{"c.php"}, {"d.php", "e.php"}, {"a.php", "b.php"}}
	if !slices.EqualFunc(chunks, expected, slices.Equal) {
		t.Errorf("expected %v, got %v", expected, chunks)
	}
}

func TestChunkFilesWithoutTimings(t *testing.T) {
	chunks := chunkFiles([]string{"a.php", "b.php", "c.php", "d.php"}, map[string]float64{}, 2)

	expected := [][]string{{"a.php", "b.php"}, {"c.php", "d.php"}}
	if !slices.EqualFunc(chunks, expected, slices.Equal) {
		t.Errorf("expected %v, got %v", expected, chunks)
	}
}

func TestTimingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), timingsFile)

	timings, err := loadTimings(path)
	if err != nil || len(timings) != 0 {
		t.Fatalf("expected empty timings for missing file, got %v, %v", timings, err)
	}

	if err := saveTimings(path, map[string]float64{"tests/FooTest.php": 1.5}); err != nil {
		t.Fatal(err)
	}

	timings, err = loadTimings(path)
	if err != nil || timings["tests/FooTest.php"] != 1.5 {
		t.Errorf("unexpected timings %v, %v", timings, err)
	}
}
