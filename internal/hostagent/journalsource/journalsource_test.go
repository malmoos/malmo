package journalsource

import "testing"

func TestParseEntry_StringMessageStdout(t *testing.T) {
	line, ok := parseEntry([]byte(`{"__REALTIME_TIMESTAMP":"1700000000000000","PRIORITY":"6","MESSAGE":"hello"}`))
	if !ok {
		t.Fatal("want ok")
	}
	if line.Line != "hello" {
		t.Errorf("line: want hello, got %q", line.Line)
	}
	if line.Stream != "stdout" {
		t.Errorf("priority 6 must map to stdout, got %q", line.Stream)
	}
	if line.Ts != "2023-11-14T22:13:20Z" {
		t.Errorf("ts: want 2023-11-14T22:13:20Z, got %q", line.Ts)
	}
}

func TestParseEntry_Priority3IsStderr(t *testing.T) {
	line, ok := parseEntry([]byte(`{"PRIORITY":"3","MESSAGE":"boom"}`))
	if !ok || line.Stream != "stderr" {
		t.Fatalf("priority 3 must map to stderr, got %+v ok=%v", line, ok)
	}
}

// journald serialises a non-UTF-8 MESSAGE as a JSON array of byte values.
func TestParseEntry_ByteArrayMessage(t *testing.T) {
	line, ok := parseEntry([]byte(`{"PRIORITY":"6","MESSAGE":[104,105]}`))
	if !ok || line.Line != "hi" {
		t.Fatalf("byte-array message decode: got %q ok=%v", line.Line, ok)
	}
}

func TestParseEntry_GarbageSkipped(t *testing.T) {
	if _, ok := parseEntry([]byte(`not json`)); ok {
		t.Fatal("an unparseable line must be skipped (ok=false)")
	}
}

func TestRealtimeToRFC3339_Unparseable(t *testing.T) {
	if got := realtimeToRFC3339("not-a-number"); got != "" {
		t.Errorf("unparseable timestamp must yield empty, got %q", got)
	}
}
