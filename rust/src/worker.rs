use std::collections::{HashMap, HashSet};
use std::io::{self, BufRead, BufReader, PipeReader, PipeWriter, Write};
use std::os::fd::AsRawFd;
use std::os::unix::process::CommandExt;
use std::process::{Child, ChildStdin, Command, Stdio};
use std::sync::Mutex;
use std::sync::mpsc::{self, Receiver};

use crate::teamcity::{Message, parse_message, test_name, test_path};

const OUTPUT_MARKER: &[u8] = b"\0blink-eof\0\n";

/// Process groups of running workers, killed on Ctrl+C.
static GROUPS: Mutex<Vec<i32>> = Mutex::new(Vec::new());

pub fn kill_all() {
    for pid in GROUPS.lock().unwrap().iter() {
        unsafe { libc::kill(-pid, libc::SIGKILL) };
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub enum Status {
    Passed,
    Failed,
    Skipped,
}

pub struct TestResult {
    pub name: String,
    pub file: String,
    pub status: Status,
    pub message: String,
    pub details: String,
}

/// Outcome of one batch of test files run in a single PHPUnit process.
#[derive(Default)]
pub struct RunResult {
    pub files: Vec<String>,
    pub tests: Vec<TestResult>,
    /// files that have at least one test started, the rest did not run, e.g. after a crash
    pub started: HashSet<String>,
    /// summed test durations in seconds per file
    pub durations: HashMap<String, f64>,
    /// set when the run failed outside of a test, e.g. crash, fatal error or broken config
    pub problem: String,
    pub output: String,
    /// set when the process ended early, so some files may not have run
    pub crashed: bool,
    /// set when the run stopped at a file another worker took
    pub stopped: bool,
}

impl RunResult {
    pub fn new(files: Vec<String>) -> Self {
        RunResult {
            files,
            ..Default::default()
        }
    }

    fn has_failures(&self) -> bool {
        self.tests.iter().any(|test| test.status == Status::Failed)
    }

    /// Detects files with no matching tests, e.g. with --filter; PHPUnit 12+ exits with 1 then.
    fn no_tests_executed(&self) -> bool {
        self.tests.is_empty() && self.output.contains("No tests executed!")
    }

    /// Files of the run that have no started test.
    pub fn not_started(&self) -> Vec<String> {
        self.files
            .iter()
            .filter(|file| !self.started.contains(*file))
            .cloned()
            .collect()
    }
}

/// The worker process is gone and has to be replaced.
pub struct WorkerDied;

/// One long-running "php worker.php" process that runs test files one by one.
pub struct Worker {
    child: Child,
    stdin: Option<ChildStdin>,
    control: PipeWriter,
    events: BufReader<PipeReader>,
    output: Receiver<String>,
}

impl Worker {
    pub fn start(
        php: &str,
        script: &str,
        autoload_file: &str,
        phpunit_args: &[String],
    ) -> io::Result<Worker> {
        let (events_read, events_write) = io::pipe()?;
        let (output_read, output_write) = io::pipe()?;
        let (control_read, control_write) = io::pipe()?;

        let mut child = {
            let events_fd = events_write.as_raw_fd();
            let control_fd = control_read.as_raw_fd();
            let mut cmd = Command::new(php);
            cmd.arg(script)
                .arg(autoload_file)
                .args(phpunit_args)
                .stdin(Stdio::piped())
                .stdout(output_write.try_clone()?)
                .stderr(output_write)
                // own process group, so a kill also reaches the forked child running the tests
                .process_group(0);

            // fd 3 and 4 in the worker
            unsafe {
                cmd.pre_exec(move || {
                    // keep control out of the way of fd 3
                    let control_fd = if control_fd == 3 {
                        libc::dup(control_fd)
                    } else {
                        control_fd
                    };
                    move_fd(events_fd, 3)?;
                    move_fd(control_fd, 4)
                });
            }

            let child = cmd.spawn()?;
            // the worker holds its pipe ends now, dropping cmd closes ours
            drop(events_write);
            drop(control_read);
            child
        };

        GROUPS.lock().unwrap().push(child.id() as i32);

        let (sender, output) = mpsc::channel();
        std::thread::spawn(move || read_output(output_read, sender));

        let mut worker = Worker {
            stdin: child.stdin.take(),
            child,
            control: control_write,
            events: BufReader::new(events_read),
            output,
        };

        // wait until the worker has booted, so its boot time is not counted to the first file
        while let Some(msg) = worker.next_message() {
            if msg.kind == "blink" && msg.name == "ready" {
                return Ok(worker);
            }
        }

        let output = worker.drain_output();
        Err(io::Error::other(format!(
            "worker failed to boot: {}",
            output.trim()
        )))
    }

    /// Returns the next TeamCity message, or None once the worker closed fd 3.
    fn next_message(&mut self) -> Option<Message> {
        let mut line = Vec::new();
        loop {
            line.clear();
            match self.events.read_until(b'\n', &mut line) {
                Ok(0) | Err(_) => return None,
                Ok(_) => {
                    if let Some(msg) = parse_message(&String::from_utf8_lossy(&line)) {
                        return Some(msg);
                    }
                }
            }
        }
    }

    /// Sends test files to the worker, which runs them in one PHPUnit process, and collects the results.
    /// Before each file the run asks claim whether it may still run it.
    pub fn run(
        &mut self,
        files: Vec<String>,
        mut claim: impl FnMut(&str) -> bool,
    ) -> (RunResult, Result<(), WorkerDied>) {
        let mut result = RunResult::new(files);

        // PHPUnit reports real absolute paths
        let file_by_path: HashMap<String, String> = result
            .files
            .iter()
            .map(|file| (real_path(file), file.clone()))
            .collect();

        let line = result.files.join("\t") + "\n";
        if self
            .stdin
            .as_mut()
            .is_none_or(|stdin| stdin.write_all(line.as_bytes()).is_err())
        {
            result.problem = "worker died before running the files".to_string();
            result.crashed = true;
            result.output = self.drain_output();
            return (result, Err(WorkerDied));
        }

        let mut current: Option<TestResult> = None;

        // a test that never finished crashed its process, blame it
        let crashed = |result: &mut RunResult,
                       current: &mut Option<TestResult>,
                       message: String,
                       output: String| {
            if let Some(mut test) = current.take() {
                result.crashed = true;
                test.status = Status::Failed;
                test.message = message;
                test.details = output;
                result.tests.push(test);
            }
        };

        while let Some(msg) = self.next_message() {
            match msg.name.as_str() {
                "claim" if msg.kind == "blink" => {
                    let file = file_by_path
                        .get(msg.attr("file"))
                        .map_or("", String::as_str);
                    let allowed = claim(file);
                    result.stopped |= !allowed;
                    let _ = self.control.write_all(if allowed { b"1" } else { b"0" });
                }
                "testStarted" => {
                    // PHPUnit 9 runs each file in its own process, so the next file starts after a crash
                    crashed(
                        &mut result,
                        &mut current,
                        "process crashed during test".to_string(),
                        String::new(),
                    );

                    let file = file_by_path
                        .get(&test_path(&msg))
                        .cloned()
                        .unwrap_or_default();
                    result.started.insert(file.clone());
                    current = Some(TestResult {
                        name: test_name(&msg),
                        file,
                        status: Status::Passed,
                        message: String::new(),
                        details: String::new(),
                    });
                }
                "testFailed" => {
                    if let Some(test) = current.as_mut() {
                        test.status = Status::Failed;
                        test.message = msg.attr("message").to_string();
                        test.details = msg.attr("details").to_string();
                    }
                }
                // a skipped class comes as "classSkipped" with the number of its tests
                "testIgnored" => {
                    if let Some(test) = current.as_mut() {
                        test.status = Status::Skipped;
                        test.message = msg.attr("message").to_string();
                    }
                }
                "testFinished" => {
                    if let Some(test) = current.take() {
                        let milliseconds: f64 = msg.attr("duration").parse().unwrap_or(0.0);
                        *result.durations.entry(test.file.clone()).or_default() +=
                            milliseconds / 1000.0;
                        result.tests.push(test);
                    }
                }
                "classSkipped" => {
                    let count: usize = msg.attr("count").parse().unwrap_or(0);
                    for _ in 0..count {
                        result.tests.push(TestResult {
                            name: msg.attr("name").to_string(),
                            file: String::new(),
                            status: Status::Skipped,
                            message: msg.attr("message").to_string(),
                            details: String::new(),
                        });
                    }
                }
                "done" if msg.kind == "blink" => {
                    result.output = self.output.recv().unwrap_or_default();
                    let exit_code: i32 = msg.attr("exit").parse().unwrap_or(0);
                    let signal: i32 = msg.attr("signal").parse().unwrap_or(0);

                    let problem = if signal != 0 {
                        format!("process killed by signal {signal}")
                    } else if exit_code != 0 {
                        format!("process exited with code {exit_code}")
                    } else {
                        String::new()
                    };

                    if current.is_some() {
                        let output = result.output.clone();
                        crashed(&mut result, &mut current, problem, output);
                    } else if signal != 0 {
                        result.problem = problem;
                        result.crashed = true;
                    } else if exit_code != 0
                        && !result.stopped
                        && !result.has_failures()
                        && !result.no_tests_executed()
                    {
                        result.problem = problem;
                    }

                    return (result, Ok(()));
                }
                _ => {}
            }
        }

        result.problem = "worker died".to_string();
        result.crashed = true;
        result.output = self.drain_output();
        let (problem, output) = (result.problem.clone(), result.output.clone());
        crashed(&mut result, &mut current, problem, output);

        (result, Err(WorkerDied))
    }

    fn drain_output(&mut self) -> String {
        self.kill();
        self.output.iter().collect()
    }

    fn kill(&mut self) {
        unsafe { libc::kill(-(self.child.id() as i32), libc::SIGKILL) };
        self.wait();
    }

    pub fn stop(mut self) {
        self.stdin.take();
        self.wait();
    }

    fn wait(&mut self) {
        let _ = self.child.wait();
        let pid = self.child.id() as i32;
        GROUPS.lock().unwrap().retain(|group| *group != pid);
    }
}

/// Makes fd the given target fd of the new process, without close-on-exec.
fn move_fd(fd: i32, target: i32) -> io::Result<()> {
    let result = unsafe {
        if fd == target {
            let flags = libc::fcntl(fd, libc::F_GETFD);
            libc::fcntl(fd, libc::F_SETFD, flags & !libc::FD_CLOEXEC)
        } else {
            libc::dup2(fd, target)
        }
    };

    if result == -1 {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    }
}

/// Splits the worker stdout/stderr into one chunk per run.
fn read_output(reader: PipeReader, sender: mpsc::Sender<String>) {
    let mut reader = BufReader::new(reader);
    let mut buffer = Vec::new();

    loop {
        let read = reader.read_until(b'\n', &mut buffer);

        if buffer.ends_with(OUTPUT_MARKER) {
            buffer.truncate(buffer.len() - OUTPUT_MARKER.len());
            let _ = sender.send(String::from_utf8_lossy(&buffer).into_owned());
            buffer.clear();
        }

        if matches!(read, Ok(0) | Err(_)) {
            if !buffer.is_empty() {
                let _ = sender.send(String::from_utf8_lossy(&buffer).into_owned());
            }
            return;
        }
    }
}

fn real_path(file: &str) -> String {
    std::fs::canonicalize(file)
        .or_else(|_| std::path::absolute(file))
        .map_or_else(
            |_| file.to_string(),
            |path| path.to_string_lossy().into_owned(),
        )
}
