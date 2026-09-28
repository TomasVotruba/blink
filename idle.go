package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

// watchIdleCPUs starts another worker, up to max, whenever about one allowed CPU was idle during the last
// interval. Tests that wait on sleep, network or subprocesses leave CPUs idle, an extra worker fills them.
// It needs /proc, elsewhere it does nothing.
func watchIdleCPUs(ctx context.Context, max int, addWorker func() bool) {
	cpus := allowedCPUs()
	if len(cpus) == 0 {
		return
	}

	// workers boot first
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Second):
	}
	previousIdle, previousTotal := cpuTimes(cpus)

	for max > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}

		idle, total := cpuTimes(cpus)
		if total > previousTotal && float64(idle-previousIdle)/float64(total-previousTotal)*float64(len(cpus)) >= 1 {
			if !addWorker() {
				return
			}
			max--
		}
		previousIdle, previousTotal = idle, total
	}
}

// cpuTimes sums idle and total time of the given CPUs from /proc/stat.
func cpuTimes(cpus map[int]bool) (idle, total uint64) {
	content, _ := os.ReadFile("/proc/stat")
	for line := range strings.Lines(string(content)) {
		fields := strings.Fields(line)
		name, ok := strings.CutPrefix(fields[0], "cpu")
		if !ok || name == "" {
			continue
		}
		if cpu, err := strconv.Atoi(name); err != nil || !cpus[cpu] {
			continue
		}

		for i, field := range fields[1:] {
			value, _ := strconv.ParseUint(field, 10, 64)
			total += value
			// idle and iowait
			if i == 3 || i == 4 {
				idle += value
			}
		}
	}

	return idle, total
}

// allowedCPUs returns CPUs this process may run on, e.g. "0-3,8" in /proc/self/status.
func allowedCPUs() map[int]bool {
	content, _ := os.ReadFile("/proc/self/status")
	cpus := map[int]bool{}

	for line := range strings.Lines(string(content)) {
		list, ok := strings.CutPrefix(line, "Cpus_allowed_list:")
		if !ok {
			continue
		}

		for part := range strings.SplitSeq(strings.TrimSpace(list), ",") {
			from, to, isRange := strings.Cut(part, "-")
			if !isRange {
				to = from
			}
			first, err1 := strconv.Atoi(from)
			last, err2 := strconv.Atoi(to)
			if err1 != nil || err2 != nil {
				continue
			}
			for cpu := first; cpu <= last; cpu++ {
				cpus[cpu] = true
			}
		}
	}

	return cpus
}
