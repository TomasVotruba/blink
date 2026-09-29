package main

import (
	"context"
	"sync"
	"time"
)

// scheduler hands chunks to workers from one shared queue. Once the queue is empty, a free worker takes
// not yet claimed files of a running chunk, so all workers finish about together.
type scheduler struct {
	mu      sync.Mutex
	changed *sync.Cond
	queue   [][]string
	running map[*worker]*runState
	busy    int
	ctx     context.Context
}

type runState struct {
	files     []string
	claimed   map[string]bool
	stolen    map[string]bool
	startedAt time.Time
}

// a stolen part has to take at least this long, it pays for its own PHPUnit and app boot
const minStealTime = time.Second

// stealable returns the files of the run another worker should take: the second half of its free files,
// if they likely take longer than minStealTime, estimated from how fast the run goes so far.
func (run *runState) stealable() []string {
	if len(run.claimed) == 0 {
		return nil
	}

	var free []string
	for _, file := range run.files {
		if !run.claimed[file] && !run.stolen[file] {
			free = append(free, file)
		}
	}
	half := free[len(free)/2:]

	perFile := time.Since(run.startedAt) / time.Duration(len(run.claimed))
	if len(half) == 0 || perFile*time.Duration(len(half)) < minStealTime {
		return nil
	}

	return half
}

func newScheduler(ctx context.Context, chunks [][]string) *scheduler {
	s := &scheduler{queue: chunks, running: map[*worker]*runState{}, ctx: ctx}
	s.changed = sync.NewCond(&s.mu)
	// wakes waiting workers on Ctrl+C, and regularly, as runs become worth a steal over time
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
			case <-ticker.C:
			}

			s.mu.Lock()
			finished := len(s.queue) == 0 && s.busy == 0
			s.changed.Broadcast()
			s.mu.Unlock()

			if finished || ctx.Err() != nil {
				return
			}
		}
	}()

	return s
}

// next blocks until there are files to run, it returns false once all work is done.
func (s *scheduler) next() ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for {
		if s.ctx.Err() != nil {
			return nil, false
		}

		if len(s.queue) > 0 {
			chunk := s.queue[0]
			s.queue = s.queue[1:]
			s.busy++
			return chunk, true
		}

		if files := s.steal(); len(files) > 0 {
			s.busy++
			return files, true
		}

		if s.busy == 0 {
			return nil, false
		}

		s.changed.Wait()
	}
}

// addWorker calls spawn if there is work another worker could take.
func (s *scheduler) addWorker(spawn func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// busy runs keep the worker pool open, so spawn can't race with its end
	if s.busy == 0 || (len(s.queue) == 0 && !s.canSteal()) {
		return false
	}
	spawn()

	return true
}

func (s *scheduler) canSteal() bool {
	for _, run := range s.running {
		if len(run.stealable()) > 0 {
			return true
		}
	}

	return false
}

// steal takes files from the run with the most of them to take.
func (s *scheduler) steal() []string {
	var victim *runState
	var files []string
	for _, run := range s.running {
		if stealable := run.stealable(); len(stealable) > len(files) {
			victim, files = run, stealable
		}
	}

	for _, file := range files {
		victim.stolen[file] = true
	}

	return files
}

func (s *scheduler) started(w *worker, files []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.running[w] = &runState{files: files, claimed: map[string]bool{}, stolen: map[string]bool{}, startedAt: time.Now()}
	s.changed.Broadcast()
}

// claim marks a file of the run as started, unless another worker took it already.
func (s *scheduler) claim(w *worker, file string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	run := s.running[w]
	if run == nil || run.stolen[file] {
		return false
	}
	run.claimed[file] = true

	return true
}

// finished ends the run of the worker and returns files another worker took from it.
func (s *scheduler) finished(w *worker) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	stolen := s.running[w].stolen
	delete(s.running, w)

	return stolen
}

// done marks files from next as finished, with chunks of files that still have to run.
func (s *scheduler) done(rest [][]string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.busy--
	s.queue = append(s.queue, rest...)
	s.changed.Broadcast()
}
