use std::collections::{HashMap, HashSet, VecDeque};
use std::sync::atomic::Ordering;
use std::sync::{Condvar, Mutex, MutexGuard};
use std::time::{Duration, Instant};

use crate::INTERRUPTED;

// a stolen part has to take at least this long, it pays for its own PHPUnit and app boot
const MIN_STEAL_TIME: Duration = Duration::from_secs(1);

/// Hands chunks to workers from one shared queue. Once the queue is empty, a free worker takes
/// not yet claimed files of a running chunk, so all workers finish about together.
pub struct Scheduler {
    state: Mutex<State>,
    changed: Condvar,
}

struct State {
    queue: VecDeque<Vec<String>>,
    /// runs in progress by worker id
    running: HashMap<usize, Run>,
    busy: usize,
}

struct Run {
    files: Vec<String>,
    claimed: HashSet<String>,
    stolen: HashSet<String>,
    started_at: Instant,
}

impl Run {
    /// Files another worker should take: the second half of the free files,
    /// if they likely take longer than MIN_STEAL_TIME, estimated from how fast the run goes so far.
    fn stealable(&self) -> Vec<String> {
        if self.claimed.is_empty() {
            return Vec::new();
        }

        let free: Vec<&String> = self
            .files
            .iter()
            .filter(|file| !self.claimed.contains(*file) && !self.stolen.contains(*file))
            .collect();
        let half = &free[free.len() / 2..];

        let per_file = self.started_at.elapsed() / self.claimed.len() as u32;
        if half.is_empty() || per_file * (half.len() as u32) < MIN_STEAL_TIME {
            return Vec::new();
        }

        half.iter().map(|file| file.to_string()).collect()
    }
}

impl State {
    fn is_finished(&self) -> bool {
        self.queue.is_empty() && self.busy == 0
    }

    /// Takes files from the run with the most of them to take.
    fn steal(&mut self) -> Vec<String> {
        let Some((id, files)) = self
            .running
            .iter()
            .map(|(id, run)| (*id, run.stealable()))
            .max_by_key(|(_, files)| files.len())
            .filter(|(_, files)| !files.is_empty())
        else {
            return Vec::new();
        };

        let run = self.running.get_mut(&id).unwrap();
        run.stolen.extend(files.iter().cloned());

        files
    }
}

impl Scheduler {
    pub fn new(chunks: Vec<Vec<String>>) -> Self {
        Scheduler {
            state: Mutex::new(State {
                queue: VecDeque::from(chunks),
                running: HashMap::new(),
                busy: 0,
            }),
            changed: Condvar::new(),
        }
    }

    fn lock(&self) -> MutexGuard<'_, State> {
        self.state.lock().unwrap()
    }

    /// Blocks until there are files to run, returns None once all work is done.
    pub fn next(&self) -> Option<Vec<String>> {
        let mut state = self.lock();

        loop {
            if INTERRUPTED.load(Ordering::SeqCst) {
                return None;
            }

            if let Some(chunk) = state.queue.pop_front() {
                state.busy += 1;
                return Some(chunk);
            }

            let files = state.steal();
            if !files.is_empty() {
                state.busy += 1;
                return Some(files);
            }

            if state.busy == 0 {
                return None;
            }

            // the timeout notices Ctrl+C and runs becoming worth a steal
            state = self
                .changed
                .wait_timeout(state, Duration::from_millis(100))
                .unwrap()
                .0;
        }
    }

    /// Calls spawn if there is work another worker could take, returns false once there is none.
    pub fn add_worker(&self, spawn: impl FnOnce()) -> bool {
        let state = self.lock();
        if state.busy == 0
            || (state.queue.is_empty()
                && state.running.values().all(|run| run.stealable().is_empty()))
        {
            return false;
        }

        spawn();
        true
    }

    /// Waits up to the timeout, returns true as soon as all work is done.
    pub fn wait_finished(&self, timeout: Duration) -> bool {
        let deadline = Instant::now() + timeout;
        let mut state = self.lock();

        while !state.is_finished() {
            let now = Instant::now();
            if now >= deadline {
                return false;
            }
            state = self.changed.wait_timeout(state, deadline - now).unwrap().0;
        }

        true
    }

    pub fn started(&self, worker: usize, files: &[String]) {
        self.lock().running.insert(
            worker,
            Run {
                files: files.to_vec(),
                claimed: HashSet::new(),
                stolen: HashSet::new(),
                started_at: Instant::now(),
            },
        );
        self.changed.notify_all();
    }

    /// Marks a file of the run as started, unless another worker took it already.
    pub fn claim(&self, worker: usize, file: &str) -> bool {
        let mut state = self.lock();
        let Some(run) = state.running.get_mut(&worker) else {
            return false;
        };
        if run.stolen.contains(file) {
            return false;
        }

        run.claimed.insert(file.to_string());
        true
    }

    /// Ends the run of the worker and returns files another worker took from it.
    pub fn finished(&self, worker: usize) -> HashSet<String> {
        self.lock()
            .running
            .remove(&worker)
            .map(|run| run.stolen)
            .unwrap_or_default()
    }

    /// Marks files from next as done, with chunks of files that still have to run.
    pub fn done(&self, rest: Vec<Vec<String>>) {
        let mut state = self.lock();
        state.busy -= 1;
        state.queue.extend(rest);
        drop(state);
        self.changed.notify_all();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn strings(values: &[&str]) -> Vec<String> {
        values.iter().map(|value| value.to_string()).collect()
    }

    #[test]
    fn steals_from_slow_run() {
        let scheduler = Scheduler::new(vec![strings(&[
            "a.php", "b.php", "c.php", "d.php", "e.php",
        ])]);

        let files = scheduler.next().unwrap();
        scheduler.started(0, &files);
        scheduler.claim(0, "a.php");
        // a.php took 10 seconds, so the rest is worth another worker
        scheduler.lock().running.get_mut(&0).unwrap().started_at =
            Instant::now() - Duration::from_secs(10);

        assert_eq!(scheduler.next(), Some(strings(&["d.php", "e.php"])));
        assert!(scheduler.claim(0, "b.php"));
        assert!(!scheduler.claim(0, "d.php"));
        assert_eq!(
            scheduler.finished(0),
            HashSet::from(["d.php".to_string(), "e.php".to_string()])
        );
    }

    #[test]
    fn keeps_fast_run() {
        let scheduler = Scheduler::new(vec![strings(&["a.php", "b.php", "c.php"])]);

        let files = scheduler.next().unwrap();
        scheduler.started(0, &files);
        scheduler.claim(0, "a.php");

        // a fast run is not worth another PHPUnit boot
        assert!(!scheduler.add_worker(|| panic!("no worker expected")));

        scheduler.finished(0);
        scheduler.done(Vec::new());
        assert_eq!(scheduler.next(), None);
    }
}
