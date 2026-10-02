package ingest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func raw(events ...string) []json.RawMessage {
	messages := make([]json.RawMessage, len(events))
	for i, event := range events {
		messages[i] = json.RawMessage(event)
	}
	return messages
}

func testOrigin() origin {
	user := int64(42)
	return origin{
		source:     "telemetry",
		client:     "tab-a",
		user:       &user,
		receivedAt: time.Date(2026, 10, 2, 12, 30, 0, 123456789, time.UTC),
	}
}

func decodeLines(t *testing.T, lines []byte) []map[string]any {
	t.Helper()
	var decoded []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(lines))
	for decoder.More() {
		var line map[string]any
		if err := decoder.Decode(&line); err != nil {
			t.Fatalf("stored line is not JSON: %v", err)
		}
		decoded = append(decoded, line)
	}
	return decoded
}

func TestEncodeBatchStoresValidEventsWithServerFields(t *testing.T) {
	lines, stored, rejected := encodeBatch(raw(
		`{"id":"s:1","stream":"app","at":1790829406415,"data":{"operation":"app.route_ready","elapsedMs":12}}`,
	), 0, testOrigin())

	if stored != 1 || len(rejected) != 0 {
		t.Fatalf("stored = %d, rejected = %v; want 1 and none", stored, rejected)
	}
	decoded := decodeLines(t, lines)
	if len(decoded) != 1 {
		t.Fatalf("lines = %d, want 1", len(decoded))
	}
	line := decoded[0]
	want := map[string]any{
		"receivedAt": "2026-10-02T12:30:00.123456789Z",
		"source":     "telemetry",
		"client":     "tab-a",
		"user":       float64(42),
		"id":         "s:1",
		"stream":     "app",
		"at":         float64(1790829406415),
	}
	for field, value := range want {
		if line[field] != value {
			t.Errorf("%s = %v, want %v", field, line[field], value)
		}
	}
	data, _ := line["data"].(map[string]any)
	if data["operation"] != "app.route_ready" || data["elapsedMs"] != float64(12) {
		t.Errorf("data = %v, want the client's object untouched", line["data"])
	}
	if _, present := line["droppedBefore"]; present {
		t.Error("droppedBefore present although the client lost nothing")
	}
}

func TestEncodeBatchOmitsTheUserOfKeySources(t *testing.T) {
	from := testOrigin()
	from.user = nil

	lines, _, _ := encodeBatch(raw(`{"id":"a","stream":"diagnostic","at":1,"data":{}}`), 0, from)

	if _, present := decodeLines(t, lines)[0]["user"]; present {
		t.Fatal("a source without users stored a user field")
	}
}

func TestEncodeBatchIgnoresFieldsTheClientHasNoSayIn(t *testing.T) {
	lines, _, _ := encodeBatch(raw(
		`{"id":"a","stream":"s","at":1,"data":{},"receivedAt":"1999","source":"evil","client":"evil","user":1,"droppedBefore":9}`,
	), 0, testOrigin())

	line := decodeLines(t, lines)[0]
	if line["source"] != "telemetry" || line["client"] != "tab-a" || line["user"] != float64(42) {
		t.Fatalf("server fields were taken from the client: %v", line)
	}
	if line["receivedAt"] == "1999" {
		t.Fatal("receivedAt was taken from the client")
	}
	if _, present := line["droppedBefore"]; present {
		t.Fatal("droppedBefore was taken from the client")
	}
}

func TestEncodeBatchRejectsEachBadEventAloneAndKeepsTheRest(t *testing.T) {
	tests := []struct {
		name     string
		event    string
		wantCode string
	}{
		{"event is a string", `"text"`, reasonNotAnObject},
		{"event is an array", `[1,2]`, reasonNotAnObject},
		{"event is null", `null`, reasonNotAnObject},
		{"missing id", `{"stream":"s","at":1,"data":{}}`, reasonBadID},
		{"id is a number", `{"id":7,"stream":"s","at":1,"data":{}}`, reasonBadID},
		{"id is empty", `{"id":"","stream":"s","at":1,"data":{}}`, reasonBadID},
		{"id is null", `{"id":null,"stream":"s","at":1,"data":{}}`, reasonBadID},
		{"id is too long", `{"id":"` + strings.Repeat("x", 129) + `","stream":"s","at":1,"data":{}}`, reasonBadID},
		{"missing stream", `{"id":"a","at":1,"data":{}}`, reasonBadStream},
		{"stream in capitals", `{"id":"a","stream":"Perf","at":1,"data":{}}`, reasonBadStream},
		{"stream with a space", `{"id":"a","stream":"my stream","at":1,"data":{}}`, reasonBadStream},
		{"stream too long", `{"id":"a","stream":"` + strings.Repeat("s", 65) + `","at":1,"data":{}}`, reasonBadStream},
		{"missing at", `{"id":"a","stream":"s","data":{}}`, reasonBadTime},
		{"at is zero", `{"id":"a","stream":"s","at":0,"data":{}}`, reasonBadTime},
		{"at is negative", `{"id":"a","stream":"s","at":-5,"data":{}}`, reasonBadTime},
		{"at is a fraction", `{"id":"a","stream":"s","at":1.5,"data":{}}`, reasonBadTime},
		{"at is an exponent", `{"id":"a","stream":"s","at":1e3,"data":{}}`, reasonBadTime},
		{"at is a quoted number", `{"id":"a","stream":"s","at":"123","data":{}}`, reasonBadTime},
		{"at overflows", `{"id":"a","stream":"s","at":99999999999999999999,"data":{}}`, reasonBadTime},
		{"missing data", `{"id":"a","stream":"s","at":1}`, reasonBadData},
		{"data is an array", `{"id":"a","stream":"s","at":1,"data":[]}`, reasonBadData},
		{"data is a string", `{"id":"a","stream":"s","at":1,"data":"x"}`, reasonBadData},
		{"data is null", `{"id":"a","stream":"s","at":1,"data":null}`, reasonBadData},
		{"event too large", `{"id":"a","stream":"s","at":1,"data":{"blob":"` + strings.Repeat("x", maxEventBytes) + `"}}`, reasonTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			good := `{"id":"good","stream":"s","at":5,"data":{"ok":true}}`

			lines, stored, rejected := encodeBatch(raw(good, tt.event, good), 0, testOrigin())

			if stored != 2 {
				t.Fatalf("stored = %d, want 2: a bad event must not cost the good ones their place", stored)
			}
			if len(rejected) != 1 || rejected[0].Index != 1 || rejected[0].Code != tt.wantCode || rejected[0].Message == "" {
				t.Fatalf("rejected = %+v, want one rejection of index 1 with code %q and a message", rejected, tt.wantCode)
			}
			if got := len(decodeLines(t, lines)); got != 2 {
				t.Fatalf("stored lines = %d, want 2", got)
			}
		})
	}
}

func TestEncodeBatchStampsDroppedOnTheFirstStoredEventOnly(t *testing.T) {
	lines, stored, rejected := encodeBatch(raw(
		`{"id":"bad","stream":"s","at":0,"data":{}}`,
		`{"id":"one","stream":"s","at":1,"data":{}}`,
		`{"id":"two","stream":"s","at":2,"data":{}}`,
	), 3, testOrigin())

	if stored != 2 || len(rejected) != 1 {
		t.Fatalf("stored = %d, rejected = %v; want 2 and one", stored, rejected)
	}
	decoded := decodeLines(t, lines)
	if decoded[0]["droppedBefore"] != float64(3) {
		t.Fatalf("first stored event droppedBefore = %v, want 3", decoded[0]["droppedBefore"])
	}
	if _, present := decoded[1]["droppedBefore"]; present {
		t.Fatal("second stored event carries droppedBefore")
	}
}

func TestEncodeBatchWritesOneLinePerEventWhateverTheClientsFormatting(t *testing.T) {
	lines, stored, _ := encodeBatch(raw(
		"{\n  \"id\": \"a\",\n  \"stream\": \"s\",\n  \"at\": 1,\n  \"data\": {\n    \"note\": \"line one\\nline two <b> & more\"\n  }\n}",
	), 0, testOrigin())

	if stored != 1 {
		t.Fatalf("stored = %d, want 1", stored)
	}
	if got := bytes.Count(lines, []byte("\n")); got != 1 || !bytes.HasSuffix(lines, []byte("\n")) {
		t.Fatalf("stored %d newlines in %q, want exactly one, at the end", got, lines)
	}
	if !bytes.Contains(lines, []byte("<b> & more")) {
		t.Fatalf("line %q escaped HTML characters; the file is meant to be read as is", lines)
	}
}

func TestEncodeBatchOfNothingValidProducesNoLines(t *testing.T) {
	lines, stored, rejected := encodeBatch(raw(`null`, `{}`), 0, testOrigin())

	if len(lines) != 0 || stored != 0 || len(rejected) != 2 {
		t.Fatalf("lines = %q, stored = %d, rejected = %v; want nothing stored and two rejections", lines, stored, rejected)
	}
}
