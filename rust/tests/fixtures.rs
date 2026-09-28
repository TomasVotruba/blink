use std::path::Path;
use std::process::Command;

// runs the fixture suite against every PHPUnit version installed in fixtures/, see README.md
#[test]
fn fixtures() {
    for version in ["9", "10", "11", "12", "13"] {
        let dir = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../fixtures")
            .join(format!("phpunit-{version}"));
        if !dir.join("vendor/autoload.php").exists() {
            eprintln!(
                "PHPUnit {version}: skipped, run composer install in {}",
                dir.display()
            );
            continue;
        }

        let output = Command::new(env!("CARGO_BIN_EXE_blink-rs"))
            .args(["-j", "3"])
            .current_dir(&dir)
            .output()
            .unwrap();
        let _ = std::fs::remove_file(dir.join(".blink-timings.json"));

        let stdout = String::from_utf8_lossy(&output.stdout);
        let stderr = String::from_utf8_lossy(&output.stderr);
        assert_eq!(
            output.status.code(),
            Some(1),
            "PHPUnit {version}: unexpected exit code\n{stdout}{stderr}"
        );

        for expected in [
            "Tests: 13, Passed: 9, Failed: 3, Skipped: 1, Broken runs: 0",
            "Fixture\\FailingTest::testFails\nFailed asserting that 2 is identical to 1.",
            "Fixture\\ErrorTest::testThrows\nRuntimeException", // PHPUnit 9 adds a space before ":"
            "Something broke",
            "Fixture\\CrashTest::testCrash\nprocess exited with code",
            "about to crash",
        ] {
            assert!(
                stdout.contains(expected),
                "PHPUnit {version}: expected output to contain {expected:?}\n{stdout}"
            );
        }
    }
}
