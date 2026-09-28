use std::io::Write;
use std::time::Duration;

use crate::worker::{RunResult, Status, TestResult};

const LINE_WIDTH: usize = 80;

/// Prints one character per test while files finish, then details of failures.
pub struct Report<W: Write> {
    out: W,
    column: usize,
    failures: Vec<String>,
    problems: Vec<String>,
    passed: usize,
    failed: usize,
    skipped: usize,
}

impl<W: Write> Report<W> {
    pub fn new(out: W) -> Self {
        Report {
            out,
            column: 0,
            failures: Vec::new(),
            problems: Vec::new(),
            passed: 0,
            failed: 0,
            skipped: 0,
        }
    }

    pub fn add(&mut self, result: &RunResult) {
        for test in &result.tests {
            match test.status {
                Status::Passed => {
                    self.passed += 1;
                    self.progress('.');
                }
                Status::Failed => {
                    self.failed += 1;
                    self.progress('F');
                    self.failures.push(format_failure(test));
                }
                Status::Skipped => {
                    self.skipped += 1;
                    self.progress('S');
                }
            }
        }

        if !result.problem.is_empty() {
            self.progress('E');
            self.problems.push(format_problem(result));
        }

        let _ = self.out.flush();
    }

    fn progress(&mut self, char: char) {
        if self.column == LINE_WIDTH {
            let _ = writeln!(self.out);
            self.column = 0;
        }

        let _ = write!(self.out, "{char}");
        self.column += 1;
    }

    /// Prints failures and the summary, and returns the exit code.
    pub fn finish(mut self, elapsed: Duration) -> i32 {
        let _ = write!(self.out, "\n\n");

        for (i, failure) in self.failures.iter().chain(&self.problems).enumerate() {
            let _ = writeln!(self.out, "{}) {failure}", i + 1);
        }

        let _ = writeln!(
            self.out,
            "Tests: {}, Passed: {}, Failed: {}, Skipped: {}, Broken runs: {} - {:.2}s",
            self.passed + self.failed + self.skipped,
            self.passed,
            self.failed,
            self.skipped,
            self.problems.len(),
            elapsed.as_secs_f64()
        );

        if self.failed > 0 || !self.problems.is_empty() {
            1
        } else {
            0
        }
    }
}

fn format_failure(test: &TestResult) -> String {
    let mut text = format!("{}\n{}\n", test.name, test.message);
    let details = test.details.trim();
    if !details.is_empty() {
        text += &format!("\n{details}\n");
    }

    text
}

fn format_problem(result: &RunResult) -> String {
    let mut files = result.files[0].clone();
    if result.files.len() > 1 {
        files += &format!(" and {} more files", result.files.len() - 1);
    }

    let mut text = format!("{files}: {}\n", result.problem);
    let output = result.output.trim();
    if !output.is_empty() {
        text += &format!("\n{output}\n");
    }

    text
}
