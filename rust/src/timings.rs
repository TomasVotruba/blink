use std::collections::{BTreeMap, HashMap};
use std::io;

use serde_json::ser::{PrettyFormatter, Serializer};

pub const TIMINGS_FILE: &str = ".blink-timings.json";

/// Returns seconds per test file from the previous run.
pub fn load_timings(path: &str) -> io::Result<HashMap<String, f64>> {
    let content = match std::fs::read(path) {
        Ok(content) => content,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(HashMap::new()),
        Err(err) => return Err(err),
    };

    Ok(serde_json::from_slice(&content)?)
}

pub fn save_timings(path: &str, timings: &HashMap<String, f64>) -> io::Result<()> {
    // sorted keys keep the file diffable, same as the Go version
    let sorted: BTreeMap<_, _> = timings.iter().collect();

    let mut content = Vec::new();
    let mut serializer =
        Serializer::with_formatter(&mut content, PrettyFormatter::with_indent(b"    "));
    serde::Serialize::serialize(&sorted, &mut serializer)?;
    content.push(b'\n');

    std::fs::write(path, content)
}

/// Splits files into about count chunks of similar total duration, slowest chunk first.
/// Neighbour files stay together, as they often share fixtures and warm caches.
/// Files without a known duration count as an average one.
pub fn chunk_files(
    files: &[String],
    timings: &HashMap<String, f64>,
    count: usize,
) -> Vec<Vec<String>> {
    let known: Vec<f64> = files
        .iter()
        .filter_map(|file| timings.get(file).copied())
        .collect();
    let sum: f64 = known.iter().sum();
    let average = if !known.is_empty() && sum > 0.0 {
        sum / known.len() as f64
    } else {
        1.0
    };

    let weight = |file: &String| timings.get(file).copied().unwrap_or(average);

    let total: f64 = files.iter().map(weight).sum();
    let target = total / count as f64;

    let mut chunks: Vec<(Vec<String>, f64)> = Vec::new();
    let mut chunk: Vec<String> = Vec::new();
    let mut chunk_weight = 0.0;

    for file in files {
        // a heavy file starts its own chunk instead of overfilling the current one
        if !chunk.is_empty() && chunk_weight + weight(file) > target {
            chunks.push((std::mem::take(&mut chunk), chunk_weight));
            chunk_weight = 0.0;
        }

        chunk.push(file.clone());
        chunk_weight += weight(file);

        if chunk_weight >= target {
            chunks.push((std::mem::take(&mut chunk), chunk_weight));
            chunk_weight = 0.0;
        }
    }
    if !chunk.is_empty() {
        chunks.push((chunk, chunk_weight));
    }

    // stable, so equal chunks keep their order
    chunks.sort_by(|a, b| b.1.total_cmp(&a.1));

    chunks.into_iter().map(|(chunk, _)| chunk).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn strings(values: &[&str]) -> Vec<String> {
        values.iter().map(|value| value.to_string()).collect()
    }

    #[test]
    fn chunks_by_timings() {
        let files = strings(&["a.php", "b.php", "c.php", "d.php", "e.php"]);
        let timings = HashMap::from([
            ("a.php".to_string(), 1.0),
            ("b.php".to_string(), 1.0),
            ("c.php".to_string(), 4.0),
            ("d.php".to_string(), 1.0),
        ]);

        // e.php is unknown, so it counts as the average of 1.75
        let chunks = chunk_files(&files, &timings, 3);

        assert_eq!(
            chunks,
            vec![
                strings(&["c.php"]),
                strings(&["d.php", "e.php"]),
                strings(&["a.php", "b.php"])
            ]
        );
    }

    #[test]
    fn chunks_without_timings() {
        let chunks = chunk_files(
            &strings(&["a.php", "b.php", "c.php", "d.php"]),
            &HashMap::new(),
            2,
        );

        assert_eq!(
            chunks,
            vec![strings(&["a.php", "b.php"]), strings(&["c.php", "d.php"])]
        );
    }

    #[test]
    fn timings_round_trip() {
        let dir = std::env::temp_dir().join(format!("blink-timings-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join(TIMINGS_FILE);
        let path = path.to_str().unwrap();

        assert!(load_timings(path).unwrap().is_empty());

        save_timings(
            path,
            &HashMap::from([("tests/FooTest.php".to_string(), 1.5)]),
        )
        .unwrap();
        assert_eq!(load_timings(path).unwrap()["tests/FooTest.php"], 1.5);

        std::fs::remove_dir_all(dir).unwrap();
    }
}
