package main

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestSchedulerStealsFromSlowRun(t *testing.T) {
	s := newScheduler(context.Background(), [][]string{{"a.php", "b.php", "c.php", "d.php", "e.php"}})
	w := &worker{}

	files, _ := s.next()
	s.started(w, files)
	s.claim(w, "a.php")
	// a.php took 10 seconds, so the rest is worth another worker
	s.running[w].startedAt = time.Now().Add(-10 * time.Second)

	stolen, ok := s.next()
	if !ok || !slices.Equal(stolen, []string{"d.php", "e.php"}) {
		t.Fatalf("expected second half of free files, got %v", stolen)
	}

	if !s.claim(w, "b.php") || s.claim(w, "d.php") {
		t.Error("expected b.php to stay with the run and d.php to be taken")
	}

	if taken := s.finished(w); !taken["d.php"] || !taken["e.php"] || len(taken) != 2 {
		t.Errorf("unexpected stolen files %v", taken)
	}
}

func TestSchedulerKeepsFastRun(t *testing.T) {
	s := newScheduler(context.Background(), [][]string{{"a.php", "b.php", "c.php"}})
	w := &worker{}

	files, _ := s.next()
	s.started(w, files)
	s.claim(w, "a.php")

	if s.canSteal() {
		t.Error("a fast run is not worth another PHPUnit boot")
	}

	s.finished(w)
	s.done(nil)
	if _, ok := s.next(); ok {
		t.Error("expected no more work")
	}
}
