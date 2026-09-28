use std::collections::HashSet;
use std::time::Duration;

use crate::scheduler::Scheduler;

/// Starts another worker via add_worker, up to max, whenever about one allowed CPU was idle during the last
/// interval. Tests that wait on sleep, network or subprocesses leave CPUs idle, an extra worker fills them.
/// It needs /proc, elsewhere it does nothing.
pub fn watch_idle_cpus(
    scheduler: &Scheduler,
    mut max: usize,
    mut add_worker: impl FnMut() -> bool,
) {
    let cpus = allowed_cpus();
    if max == 0 || cpus.is_empty() {
        return;
    }

    // workers boot first
    if scheduler.wait_finished(Duration::from_secs(1)) {
        return;
    }
    let (mut previous_idle, mut previous_total) = cpu_times(&cpus);

    while max > 0 {
        if scheduler.wait_finished(Duration::from_millis(250)) {
            return;
        }

        let (idle, total) = cpu_times(&cpus);
        if total > previous_total
            && (idle - previous_idle) as f64 / (total - previous_total) as f64 * cpus.len() as f64
                >= 1.0
        {
            if !add_worker() {
                return;
            }
            max -= 1;
        }
        (previous_idle, previous_total) = (idle, total);
    }
}

/// Sums idle and total time of the given CPUs from /proc/stat.
fn cpu_times(cpus: &HashSet<usize>) -> (u64, u64) {
    let content = std::fs::read_to_string("/proc/stat").unwrap_or_default();
    let (mut idle, mut total) = (0, 0);

    for line in content.lines() {
        let mut fields = line.split_whitespace();
        let Some(cpu) = fields
            .next()
            .and_then(|name| name.strip_prefix("cpu"))
            .and_then(|id| id.parse().ok())
        else {
            continue;
        };
        if !cpus.contains(&cpu) {
            continue;
        }

        for (i, value) in fields
            .map(|field| field.parse::<u64>().unwrap_or(0))
            .enumerate()
        {
            total += value;
            // idle and iowait
            if i == 3 || i == 4 {
                idle += value;
            }
        }
    }

    (idle, total)
}

/// CPUs this process may run on, e.g. "0-3,8" in /proc/self/status.
fn allowed_cpus() -> HashSet<usize> {
    let content = std::fs::read_to_string("/proc/self/status").unwrap_or_default();
    let mut cpus = HashSet::new();

    for line in content.lines() {
        let Some(list) = line.strip_prefix("Cpus_allowed_list:") else {
            continue;
        };

        for part in list.trim().split(',') {
            let (from, to) = part.split_once('-').unwrap_or((part, part));
            if let (Ok(first), Ok(last)) = (from.parse::<usize>(), to.parse::<usize>()) {
                cpus.extend(first..=last);
            }
        }
    }

    cpus
}
