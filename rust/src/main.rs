mod discover;
mod report;
mod teamcity;
mod timings;
mod worker;

use std::collections::VecDeque;
use std::io::{self, Write};
use std::path::Path;
use std::process::ExitCode;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Mutex, mpsc};
use std::time::Instant;

use report::Report;
use timings::{TIMINGS_FILE, chunk_files, load_timings, save_timings};
use worker::{RunResult, Worker};

// more chunks than workers lets a free worker pick up remaining work, fewer saves on PHPUnit and app boots
const CHUNKS_PER_WORKER: usize = 3;

const WORKER_SCRIPT: &str = include_str!("../../worker.php");

static INTERRUPTED: AtomicBool = AtomicBool::new(false);

struct Options {
    workers: usize,
    php: String,
    config: String,
    paths: Vec<String>,
    phpunit_args: Vec<String>,
}

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();

    ExitCode::from(run(args) as u8)
}

fn run(args: Vec<String>) -> i32 {
    let options = match parse_args(args) {
        Ok(options) => options,
        Err(message) => {
            if !message.is_empty() {
                eprintln!("{message}");
            }
            eprintln!("Usage: blink [options] [paths...] [-- phpunit options]");
            eprintln!(
                "  -c string\n    \tPHPUnit configuration file (default: phpunit.xml or phpunit.xml.dist)"
            );
            eprintln!(
                "  -j int\n    \tnumber of parallel workers (default {})",
                cpu_count()
            );
            eprintln!("  -php string\n    \tPHP binary (default \"php\")");
            return 2;
        }
    };

    match execute(options) {
        Ok(code) => code,
        Err(message) => {
            eprintln!("{message}");
            2
        }
    }
}

fn execute(options: Options) -> Result<i32, String> {
    let mut phpunit_args = options.phpunit_args;

    let config_path = discover::find_config(&options.config)
        .map_err(|err| format!("{}: {err}", options.config))?;
    if let Some(config_path) = &config_path {
        phpunit_args.splice(0..0, ["--configuration".to_string(), config_path.clone()]);
    }

    let files = match (&config_path, options.paths.is_empty()) {
        (_, false) => discover::discover_from_paths(&options.paths)?,
        (Some(config_path), true) => discover::discover_from_config(config_path)?,
        (None, true) => {
            return Err("no paths given and no phpunit.xml or phpunit.xml.dist found".to_string());
        }
    };
    if files.is_empty() {
        return Err("no test files found".to_string());
    }

    let autoload_file =
        std::path::absolute("vendor/autoload.php").map_err(|err| err.to_string())?;
    if !autoload_file.exists() {
        return Err("vendor/autoload.php not found, run composer install first".to_string());
    }
    let autoload_file = autoload_file.to_string_lossy().into_owned();

    let script_dir = std::env::temp_dir().join(format!("blink-{}", std::process::id()));
    std::fs::create_dir_all(&script_dir).map_err(|err| err.to_string())?;
    let _cleanup = RemoveOnDrop(&script_dir);

    let script = script_dir.join("worker.php");
    std::fs::write(&script, WORKER_SCRIPT).map_err(|err| err.to_string())?;
    let script = script.to_string_lossy().into_owned();

    let mut timings = load_timings(TIMINGS_FILE).unwrap_or_else(|err| {
        eprintln!("ignoring {TIMINGS_FILE}: {err}");
        Default::default()
    });

    // workers run in their own process groups, so they don't get Ctrl+C from the terminal
    let _ = ctrlc::set_handler(|| {
        INTERRUPTED.store(true, Ordering::SeqCst);
        worker::kill_all();
    });

    let workers = options.workers.min(files.len());
    let chunks = chunk_files(&files, &timings, workers * CHUNKS_PER_WORKER);

    let mut stdout = io::stdout();
    let _ = writeln!(stdout, "blink - {} files, {workers} workers\n", files.len());

    let start = Instant::now();
    let mut report = Report::new(stdout);

    run_chunks(
        chunks,
        workers,
        &options.php,
        &script,
        &autoload_file,
        &phpunit_args,
        |result| {
            report.add(&result);
            timings.extend(result.durations);
        },
    );

    if INTERRUPTED.load(Ordering::SeqCst) {
        eprintln!("\ninterrupted");
        return Ok(130);
    }

    if let Err(err) = save_timings(TIMINGS_FILE, &timings) {
        eprintln!("cannot save {TIMINGS_FILE}: {err}");
    }

    Ok(report.finish(start.elapsed()))
}

struct RemoveOnDrop<'a>(&'a Path);

impl Drop for RemoveOnDrop<'_> {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(self.0);
    }
}

/// Parses options like Go's flag package: "-j 4", "-j=4" or "--j 4", up to the first path.
/// PHPUnit options come after "--".
fn parse_args(args: Vec<String>) -> Result<Options, String> {
    let (args, phpunit_args) = match args.iter().position(|arg| arg == "--") {
        Some(index) => (args[..index].to_vec(), args[index + 1..].to_vec()),
        None => (args, Vec::new()),
    };

    let mut options = Options {
        workers: cpu_count(),
        php: "php".to_string(),
        config: String::new(),
        paths: Vec::new(),
        phpunit_args,
    };

    let mut args = args.into_iter();
    while let Some(arg) = args.next() {
        let Some(flag) = arg
            .strip_prefix("--")
            .or_else(|| arg.strip_prefix('-'))
            .filter(|flag| !flag.is_empty())
        else {
            options.paths.push(arg);
            options.paths.extend(args);
            break;
        };

        let (name, inline_value) = match flag.split_once('=') {
            Some((name, value)) => (name, Some(value.to_string())),
            None => (flag, None),
        };
        if name == "h" || name == "help" {
            return Err(String::new());
        }

        let mut value = || {
            inline_value
                .clone()
                .or_else(|| args.next())
                .ok_or(format!("flag needs an argument: -{name}"))
        };
        match name {
            "j" => {
                let value = value()?;
                options.workers = value
                    .parse()
                    .map_err(|_| format!("invalid value \"{value}\" for flag -j"))?;
            }
            "c" => options.config = value()?,
            "php" => options.php = value()?,
            _ => return Err(format!("flag provided but not defined: -{name}")),
        }
    }

    Ok(options)
}

fn cpu_count() -> usize {
    std::thread::available_parallelism().map_or(1, |count| count.get())
}

/// Runs chunks of files on a pool of workers. Every worker takes the next chunk from one shared queue
/// once it is free, so a fast worker picks up more chunks.
fn run_chunks(
    chunks: Vec<Vec<String>>,
    worker_count: usize,
    php: &str,
    script: &str,
    autoload_file: &str,
    phpunit_args: &[String],
    mut on_result: impl FnMut(RunResult),
) {
    let queue = Mutex::new(VecDeque::from(chunks));
    let (sender, results) = mpsc::channel();

    std::thread::scope(|scope| {
        for _ in 0..worker_count {
            let sender = sender.clone();
            let queue = &queue;

            scope.spawn(move || {
                let mut worker: Option<Worker> = None;

                loop {
                    let Some(chunk) = queue.lock().unwrap().pop_front() else {
                        break;
                    };
                    let mut pending = VecDeque::from([chunk]);

                    while let Some(files) = pending.pop_front() {
                        if INTERRUPTED.load(Ordering::SeqCst) {
                            break;
                        }

                        let current = match worker.as_mut() {
                            Some(current) => current,
                            None => match Worker::start(php, script, autoload_file, phpunit_args) {
                                Ok(started) => worker.insert(started),
                                Err(err) => {
                                    let mut result = RunResult::new(files);
                                    result.problem = format!("cannot start worker: {err}");
                                    let _ = sender.send(result);
                                    continue;
                                }
                            },
                        };

                        let (mut result, status) = current.run(files);
                        if status.is_err() {
                            // the next run gets a fresh worker
                            worker = None;
                        }

                        let reruns = rerun_files(&result);
                        if !reruns.is_empty() && result.not_started().len() == result.files.len() {
                            // nothing ran, the reruns report the problem
                            result.problem.clear();
                            result.output.clear();
                        }

                        let _ = sender.send(result);
                        pending.extend(reruns);
                    }
                }

                if let Some(worker) = worker {
                    worker.stop();
                }
            });
        }
        drop(sender);

        for result in results {
            on_result(result);
        }
    });
}

/// Returns files that did not run because their run ended early.
/// Without any progress, e.g. on a syntax error, each file runs alone to find the broken one.
fn rerun_files(result: &RunResult) -> Vec<Vec<String>> {
    if !result.crashed && result.problem.is_empty() {
        return Vec::new();
    }

    let not_started = result.not_started();
    if not_started.is_empty() {
        Vec::new()
    } else if not_started.len() < result.files.len() {
        vec![not_started]
    } else if not_started.len() > 1 {
        not_started.into_iter().map(|file| vec![file]).collect()
    } else {
        Vec::new()
    }
}
