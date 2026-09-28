package main

import (
	"testing"
)

func TestParseMessage(t *testing.T) {
	msg, ok := parseMessage(`##teamcity[testFailed name='testFails' message='It|'s |[broken|]|nreally' details=' /app/FooTest.php:18|n ' flowId='1']`)
	if !ok {
		t.Fatal("expected message to be parsed")
	}

	if msg.Kind != "teamcity" || msg.Name != "testFailed" {
		t.Errorf("unexpected kind %q and name %q", msg.Kind, msg.Name)
	}

	expected := map[string]string{
		"name":    "testFails",
		"message": "It's [broken]\nreally",
		"details": " /app/FooTest.php:18\n ",
		"flowId":  "1",
	}
	for key, value := range expected {
		if msg.Attrs[key] != value {
			t.Errorf("attribute %s: expected %q, got %q", key, value, msg.Attrs[key])
		}
	}
}

func TestParseMessageWithoutAttributes(t *testing.T) {
	msg, ok := parseMessage("##blink[ready]\n")
	if !ok || msg.Kind != "blink" || msg.Name != "ready" || len(msg.Attrs) != 0 {
		t.Errorf("unexpected message %+v", msg)
	}
}

func TestParseMessageRejectsOtherLines(t *testing.T) {
	for _, line := range []string{"", "PHPUnit 13.3.5", "##teamcity", "## not[ a message"} {
		if _, ok := parseMessage(line); ok {
			t.Errorf("expected %q to be rejected", line)
		}
	}
}

func TestTestName(t *testing.T) {
	msg, _ := parseMessage(`##teamcity[testStarted name='testAdd with data set #0' locationHint='php_qn:///app/tests/FooTest.php::\App\FooTest::testAdd with data set #0' flowId='1']`)

	if name := testName(msg); name != `App\FooTest::testAdd with data set #0` {
		t.Errorf("unexpected test name %q", name)
	}
}

func TestTestPath(t *testing.T) {
	msg, _ := parseMessage(`##teamcity[testStarted name='testBar' locationHint='php_qn:///app/tests/FooTest.php::\App\FooTest::testBar' flowId='1']`)

	if path := testPath(msg); path != "/app/tests/FooTest.php" {
		t.Errorf("unexpected test path %q", path)
	}
}
