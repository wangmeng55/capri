package transport

import (
	"bytes"
	stdflate "compress/flate"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	goccyjson "github.com/goccy/go-json"
	kflate "github.com/klauspost/compress/flate"
)

// TestEventsFrameEnvelopeJSONEquivalence guards the T3.2 swap: the typed
// envelope must produce the same DOCUMENT whether stdlib or goccy encodes
// it. Key order is not part of the JSON contract (the hub decodes into a
// map), so the assertion is on the decoded value, not the bytes.
func TestEventsFrameEnvelopeJSONEquivalence(t *testing.T) {
	evs := []Event{
		{
			"seq":  uint64(1),
			"type": "session_update",
			"text": `hello <b>"x"</b> & \n`,
		},
		{
			"seq":  uint64(2),
			"type": "tool_call",
			"n":    int64(1234567890123),
			"f":    1.5,
			"b":    true,
			"nil":  nil,
			"arr":  []any{1.0, "s"},
			"obj":  map[string]any{"k": "v"},
		},
	}
	env := eventsFrameEnvelope{V: 1, Type: "events", SeqStart: 1, Events: evs}

	stdlibBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("stdlib marshal: %v", err)
	}
	goccyBytes, err := goccyjson.Marshal(env)
	if err != nil {
		t.Fatalf("goccy marshal: %v", err)
	}

	var fromStdlib, fromGoccy any
	if err := json.Unmarshal(stdlibBytes, &fromStdlib); err != nil {
		t.Fatalf("stdlib output is not valid JSON: %v", err)
	}
	if err := json.Unmarshal(goccyBytes, &fromGoccy); err != nil {
		t.Fatalf("goccy output is not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(fromStdlib, fromGoccy) {
		t.Fatalf("semantic mismatch:\n stdlib=%s\n goccy =%s", stdlibBytes, goccyBytes)
	}

	// The production frame builder goes through goccy too; its output must
	// still decode to the same document.
	c := NewClient(Config{})
	live := c.marshalEventsFrame(evs)
	if live == nil {
		t.Fatal("marshalEventsFrame returned nil for a small batch")
	}
	var fromLive any
	if err := json.Unmarshal(live, &fromLive); err != nil {
		t.Fatalf("live frame is not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(fromLive, fromStdlib) {
		t.Fatalf("live frame mismatch:\n live  =%s\n want  =%s", live, stdlibBytes)
	}
}

// TestDeflateWireCompatibility guards T3.1: switching the writer to
// klauspost must not change the RFC 1951 byte stream in a way that breaks
// a stdlib reader, and a stdlib-written stream must still be readable by
// the klauspost reader.
func TestDeflateWireCompatibility(t *testing.T) {
	payload := bytes.Repeat([]byte(`{"v":1,"type":"events","text":"lorem ipsum dolor"}`), 400)

	// klauspost writer (transport) → stdlib reader.
	zp, ok := deflatePayload(payload)
	if !ok {
		t.Fatalf("expected compressed output for %d-byte payload", len(payload))
	}
	sr := stdflate.NewReader(bytes.NewReader(zp))
	gotStdlib, err := io.ReadAll(sr)
	if err != nil {
		t.Fatalf("stdlib inflate of klauspost stream: %v", err)
	}
	_ = sr.Close()
	if !bytes.Equal(gotStdlib, payload) {
		t.Fatal("stdlib inflate mismatch")
	}

	// stdlib writer → klauspost reader.
	var buf bytes.Buffer
	w, err := stdflate.NewWriter(&buf, stdflate.DefaultCompression)
	if err != nil {
		t.Fatalf("stdlib writer: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("stdlib write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("stdlib close: %v", err)
	}
	kr := kflate.NewReader(bytes.NewReader(buf.Bytes()))
	gotKlauspost, err := io.ReadAll(kr)
	if err != nil {
		t.Fatalf("klauspost inflate of stdlib stream: %v", err)
	}
	_ = kr.Close()
	if !bytes.Equal(gotKlauspost, payload) {
		t.Fatal("klauspost inflate mismatch")
	}
}
