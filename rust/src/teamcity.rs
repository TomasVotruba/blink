use std::collections::HashMap;

/// One "##kind[name key='value' ...]" line, as PHPUnit writes with --log-teamcity.
#[derive(Debug, Default)]
pub struct Message {
    pub kind: String,
    pub name: String,
    pub attrs: HashMap<String, String>,
}

impl Message {
    pub fn attr(&self, key: &str) -> &str {
        self.attrs.get(key).map_or("", String::as_str)
    }
}

pub fn parse_message(line: &str) -> Option<Message> {
    let line = line.trim();
    let inner = line.strip_prefix("##")?.strip_suffix(']')?;

    let (kind, rest) = inner.split_once('[')?;
    if kind.is_empty() {
        return None;
    }

    let (name, mut rest) = rest.split_once(' ').unwrap_or((rest, ""));
    let mut msg = Message {
        kind: kind.to_string(),
        name: name.to_string(),
        attrs: HashMap::new(),
    };

    loop {
        rest = rest.trim_start_matches(' ');
        let Some((key, after)) = rest.split_once("='") else {
            return Some(msg);
        };

        let (value, remaining) = read_value(after);
        msg.attrs.insert(key.to_string(), value);
        rest = remaining;
    }
}

/// Reads an escaped value up to its closing quote and returns the rest of the line.
fn read_value(s: &str) -> (String, &str) {
    let bytes = s.as_bytes();
    let mut value = Vec::with_capacity(bytes.len());
    let mut i = 0;

    while i < bytes.len() {
        match bytes[i] {
            b'\'' => return (String::from_utf8_lossy(&value).into_owned(), &s[i + 1..]),
            b'|' => {
                if i + 1 == bytes.len() {
                    break;
                }
                i += 1;
                match bytes[i] {
                    b'n' => value.push(b'\n'),
                    b'r' => value.push(b'\r'),
                    // |' |[ |] ||
                    other => value.push(other),
                }
            }
            other => value.push(other),
        }
        i += 1;
    }

    (String::from_utf8_lossy(&value).into_owned(), "")
}

/// Turns "php_qn:///path/FooTest.php::\App\FooTest::testBar" into "App\FooTest::testBar".
pub fn test_name(msg: &Message) -> String {
    match msg.attr("locationHint").split_once("::\\") {
        Some((_, name)) => name.to_string(),
        None => msg.attr("name").to_string(),
    }
}

/// Turns "php_qn:///path/FooTest.php::\App\FooTest::testBar" into "/path/FooTest.php".
pub fn test_path(msg: &Message) -> String {
    let location = msg.attr("locationHint");
    let location = location.strip_prefix("php_qn://").unwrap_or(location);

    location
        .split_once("::\\")
        .map_or(location, |(path, _)| path)
        .to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_message() {
        let msg = parse_message(
            "##teamcity[testFailed name='testFails' message='It|'s |[broken|]|nreally' details=' /app/FooTest.php:18|n ' flowId='1']",
        )
        .expect("message is parsed");

        assert_eq!(msg.kind, "teamcity");
        assert_eq!(msg.name, "testFailed");
        assert_eq!(msg.attr("name"), "testFails");
        assert_eq!(msg.attr("message"), "It's [broken]\nreally");
        assert_eq!(msg.attr("details"), " /app/FooTest.php:18\n ");
        assert_eq!(msg.attr("flowId"), "1");
    }

    #[test]
    fn parses_message_without_attributes() {
        let msg = parse_message("##blink[ready]\n").expect("message is parsed");

        assert_eq!(msg.kind, "blink");
        assert_eq!(msg.name, "ready");
        assert!(msg.attrs.is_empty());
    }

    #[test]
    fn rejects_other_lines() {
        for line in ["", "PHPUnit 13.3.5", "##teamcity", "## not[ a message"] {
            assert!(
                parse_message(line).is_none(),
                "expected {line:?} to be rejected"
            );
        }
    }

    #[test]
    fn extracts_test_name_and_path() {
        let msg = parse_message(
            "##teamcity[testStarted name='testAdd with data set #0' locationHint='php_qn:///app/tests/FooTest.php::\\App\\FooTest::testAdd with data set #0' flowId='1']",
        )
        .expect("message is parsed");

        assert_eq!(test_name(&msg), "App\\FooTest::testAdd with data set #0");
        assert_eq!(test_path(&msg), "/app/tests/FooTest.php");
    }
}
