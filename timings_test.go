package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestSortSlowestFirst(t *testing.T) {
	files := []string{"fast.php", "unknown.php", "slow.php", "medium.php"}
	timings := map[string]float64{"fast.php": 0.1, "slow.php": 3, "medium.php": 1}

	sortSlowestFirst(files, timings)

	expected := []string{"unknown.php", "slow.php", "medium.php", "fast.php"}
	if !slices.Equal(files, expected) {
		t.Errorf("expected %v, got %v", expected, files)
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
