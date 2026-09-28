package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runs the fixture suite against every PHPUnit version installed in fixtures/, see README.md
func TestFixtures(t *testing.T) {
	for _, version := range []string{"9", "10", "11", "12", "13"} {
		t.Run("PHPUnit "+version, func(t *testing.T) {
			dir, err := filepath.Abs(filepath.Join("fixtures", "phpunit-"+version))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "vendor/autoload.php")); err != nil {
				t.Skip("run composer install in " + dir)
			}

			t.Chdir(dir)
			t.Cleanup(func() { os.Remove(filepath.Join(dir, timingsFile)) })

			var stdout, stderr bytes.Buffer
			exitCode := run([]string{"-j", "3"}, &stdout, &stderr)

			if exitCode != 1 {
				t.Errorf("expected exit code 1, got %d\n%s%s", exitCode, stdout.String(), stderr.String())
			}

			for _, expected := range []string{
				"Tests: 13, Passed: 9, Failed: 3, Skipped: 1, Broken runs: 0",
				`Fixture\FailingTest::testFails` + "\nFailed asserting that 2 is identical to 1.",
				`Fixture\ErrorTest::testThrows` + "\nRuntimeException", // PHPUnit 9 adds a space before ":"
				"Something broke",
				`Fixture\CrashTest::testCrash` + "\nprocess exited with code",
				"about to crash",
			} {
				if !strings.Contains(stdout.String(), expected) {
					t.Errorf("expected output to contain %q\n%s", expected, stdout.String())
				}
			}
		})
	}
}
