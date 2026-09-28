package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDiscoverFromConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "phpunit.xml"), `<?xml version="1.0"?>
<phpunit>
    <testsuites>
        <testsuite name="unit">
            <directory>tests/Unit</directory>
            <exclude>tests/Unit/Excluded</exclude>
        </testsuite>
        <testsuite name="other">
            <directory suffix="Check.php">tests/Other</directory>
            <file>tests/Single.php</file>
        </testsuite>
    </testsuites>
</phpunit>`)

	for _, file := range []string{
		"tests/Unit/FooTest.php",
		"tests/Unit/Deep/BarTest.php",
		"tests/Unit/Helper.php",
		"tests/Unit/Excluded/SkipTest.php",
		"tests/Other/BazCheck.php",
		"tests/Other/BazTest.php",
		"tests/Single.php",
	} {
		writeFile(t, filepath.Join(dir, file), "<?php")
	}

	files, err := discoverFromConfig(filepath.Join(dir, "phpunit.xml"))
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{
		filepath.Join(dir, "tests/Other/BazCheck.php"),
		filepath.Join(dir, "tests/Single.php"),
		filepath.Join(dir, "tests/Unit/Deep/BarTest.php"),
		filepath.Join(dir, "tests/Unit/FooTest.php"),
	}
	if !slices.Equal(files, expected) {
		t.Errorf("expected %v, got %v", expected, files)
	}
}

func TestDiscoverFromPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "tests/FooTest.php"), "<?php")
	writeFile(t, filepath.Join(dir, "tests/Helper.php"), "<?php")
	writeFile(t, filepath.Join(dir, "other/Custom.php"), "<?php")

	files, err := discoverFromPaths([]string{filepath.Join(dir, "tests"), filepath.Join(dir, "other/Custom.php")})
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{filepath.Join(dir, "other/Custom.php"), filepath.Join(dir, "tests/FooTest.php")}
	if !slices.Equal(files, expected) {
		t.Errorf("expected %v, got %v", expected, files)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
