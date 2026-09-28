use std::io;
use std::path::{Component, Path, PathBuf};

const DEFAULT_SUFFIX: &str = "Test.php";

/// Returns the given config path, or phpunit.xml / phpunit.xml.dist from the current directory.
pub fn find_config(config_path: &str) -> io::Result<Option<String>> {
    if !config_path.is_empty() {
        std::fs::metadata(config_path)?;
        return Ok(Some(config_path.to_string()));
    }

    Ok(["phpunit.xml", "phpunit.xml.dist"]
        .into_iter()
        .find(|name| Path::new(name).exists())
        .map(String::from))
}

/// Collects test files from all <testsuite> elements of a PHPUnit config.
pub fn discover_from_config(config_path: &str) -> Result<Vec<String>, String> {
    let content = std::fs::read_to_string(config_path).map_err(|err| err.to_string())?;
    let document = roxmltree::Document::parse(&content)
        .map_err(|err| format!("parse {config_path}: {err}"))?;

    let base_dir = Path::new(config_path).parent().unwrap_or(Path::new(""));
    let resolve = |path: &str| clean(&base_dir.join(path.trim()));

    let suites = document
        .root_element()
        .children()
        .filter(|node| node.has_tag_name("testsuites"))
        .flat_map(|node| node.children())
        .filter(|node| node.has_tag_name("testsuite"));

    let mut files = Vec::new();
    for suite in suites {
        let children = || suite.children().filter(|node| node.is_element());
        let text = |node: roxmltree::Node| node.text().unwrap_or("").to_string();

        let excludes: Vec<String> = children()
            .filter(|node| node.has_tag_name("exclude"))
            .map(|node| resolve(&text(node)))
            .collect();

        for directory in children().filter(|node| node.has_tag_name("directory")) {
            let suffix = directory
                .attribute("suffix")
                .filter(|suffix| !suffix.is_empty())
                .unwrap_or(DEFAULT_SUFFIX);

            let pattern = resolve(&text(directory));
            let dirs = glob::glob(&pattern).map_err(|err| err.to_string())?;
            for dir in dirs.flatten() {
                walk_test_files(&clean(&dir), suffix, &excludes, &mut files)
                    .map_err(|err| err.to_string())?;
            }
        }

        for file in children().filter(|node| node.has_tag_name("file")) {
            files.push(resolve(&text(file)));
        }
    }

    Ok(unique_sorted(files))
}

/// Collects test files from paths given on the command line.
pub fn discover_from_paths(paths: &[String]) -> Result<Vec<String>, String> {
    let mut files = Vec::new();
    for path in paths {
        let metadata = std::fs::metadata(path).map_err(|err| format!("{path}: {err}"))?;

        if metadata.is_dir() {
            walk_test_files(&clean(Path::new(path)), DEFAULT_SUFFIX, &[], &mut files)
                .map_err(|err| err.to_string())?;
        } else {
            files.push(clean(Path::new(path)));
        }
    }

    Ok(unique_sorted(files))
}

fn walk_test_files(
    dir: &str,
    suffix: &str,
    excludes: &[String],
    files: &mut Vec<String>,
) -> io::Result<()> {
    for entry in std::fs::read_dir(dir)? {
        let entry = entry?;
        let path = format!("{dir}/{}", entry.file_name().to_string_lossy());

        if excludes.contains(&path) {
            continue;
        }

        // symlinked directories inside are not followed, same as the Go version
        if entry.file_type()?.is_dir() {
            walk_test_files(&path, suffix, excludes, files)?;
        } else if path.ends_with(suffix) {
            files.push(path);
        }
    }

    Ok(())
}

/// Lexical path cleaning like Go's filepath.Clean, so "./tests/" and "tests" match.
fn clean(path: &Path) -> String {
    let mut parts: Vec<Component> = Vec::new();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir if matches!(parts.last(), Some(Component::Normal(_))) => {
                parts.pop();
            }
            Component::ParentDir if matches!(parts.last(), Some(Component::RootDir)) => {}
            other => parts.push(other),
        }
    }

    let cleaned: PathBuf = parts.iter().collect();
    match cleaned.to_string_lossy() {
        path if path.is_empty() => ".".to_string(),
        path => path.into_owned(),
    }
}

fn unique_sorted(mut files: Vec<String>) -> Vec<String> {
    files.sort();
    files.dedup();
    files
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_dir(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("blink-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    fn write_file(path: &Path, content: &str) {
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(path, content).unwrap();
    }

    fn join(dir: &Path, file: &str) -> String {
        dir.join(file).to_string_lossy().into_owned()
    }

    #[test]
    fn discovers_from_config() {
        let dir = temp_dir("config");
        write_file(
            &dir.join("phpunit.xml"),
            r#"<?xml version="1.0"?>
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
</phpunit>"#,
        );

        for file in [
            "tests/Unit/FooTest.php",
            "tests/Unit/Deep/BarTest.php",
            "tests/Unit/Helper.php",
            "tests/Unit/Excluded/SkipTest.php",
            "tests/Other/BazCheck.php",
            "tests/Other/BazTest.php",
            "tests/Single.php",
        ] {
            write_file(&dir.join(file), "<?php");
        }

        let files = discover_from_config(&join(&dir, "phpunit.xml")).unwrap();

        assert_eq!(
            files,
            vec![
                join(&dir, "tests/Other/BazCheck.php"),
                join(&dir, "tests/Single.php"),
                join(&dir, "tests/Unit/Deep/BarTest.php"),
                join(&dir, "tests/Unit/FooTest.php"),
            ]
        );

        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn discovers_from_paths() {
        let dir = temp_dir("paths");
        write_file(&dir.join("tests/FooTest.php"), "<?php");
        write_file(&dir.join("tests/Helper.php"), "<?php");
        write_file(&dir.join("other/Custom.php"), "<?php");

        let files =
            discover_from_paths(&[join(&dir, "tests"), join(&dir, "other/Custom.php")]).unwrap();

        assert_eq!(
            files,
            vec![
                join(&dir, "other/Custom.php"),
                join(&dir, "tests/FooTest.php")
            ]
        );

        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn cleans_paths() {
        assert_eq!(clean(Path::new("./tests/")), "tests");
        assert_eq!(clean(Path::new("a/../b/./c")), "b/c");
        assert_eq!(clean(Path::new("")), ".");
    }
}
