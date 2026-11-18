package sandbox

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestWorkerFrameBoundsBeforePayload(t *testing.T) {
	for _, kind := range []byte{wireHello, wireEval, wireHTTP, wireReply, wireResult, 99} {
		var header [13]byte
		copy(header[:4], "YLW1")
		header[4] = kind
		binary.BigEndian.PutUint32(header[9:], frameLimit(kind)+1)
		r := bytes.NewReader(header[:])
		_, err := readWorkerFrame(r, func(byte, uint32) bool { return true })
		if !errors.Is(err, errWorkerProtocol) || r.Len() != 0 {
			t.Fatal("size not rejected in header", kind, err)
		}
	}
	var output bytes.Buffer
	if err := writeWorkerFrame(&output, wireHTTP, 1, workerHTTP{Method: "GET", URL: "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	_, err := readWorkerFrame(bytes.NewReader(output.Bytes()), func(byte, uint32) bool { return false })
	if !errors.Is(err, errWorkerProtocol) {
		t.Fatal("forbidden state accepted", err)
	}
	f, err := readWorkerFrame(&output, func(k byte, s uint32) bool { return k == wireHTTP && s == 1 })
	if err != nil || f.Seq != 1 {
		t.Fatal(f, err)
	}
	if _, err := readWorkerFrame(strings.NewReader("YLW"), func(byte, uint32) bool { return true }); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
}

func TestWorkerStrictJSON(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"ok":true,"ok":false}`, `{"OK":true}`, `{"ok":null}`, `{"ok":true,"source_id":"forged"}`, `{"ok":true} {}`, `{"ok":true,"value":` + strings.Repeat("[", 41) + strings.Repeat("]", 41) + `}`} {
		var result workerResult
		if err := decodeWorkerJSON([]byte(raw), &result, "ok"); err == nil {
			t.Fatal("invalid protocol accepted", raw[:min(len(raw), 80)])
		}
	}
	var result workerResult
	if err := decodeWorkerJSON([]byte(`{"ok":true,"value":{"n":1}}`), &result, "ok"); err != nil {
		t.Fatal(err)
	}
}

type workerMethodString string

func (workerMethodString) MarshalJSON() ([]byte, error) { panic("Go marshaler must not execute") }
func TestWorkerInputOwnershipAndNumbers(t *testing.T) {
	vars := map[string]any{"v": workerMethodString("plain"), "nested": map[string]any{"x": 1}}
	copy := jsonInputCopy(reflect.ValueOf(vars)).(map[string]any)
	if _, err := json.Marshal(copy); err != nil {
		t.Fatal(err)
	}
	copy["nested"].(map[string]any)["x"] = 2
	if vars["nested"].(map[string]any)["x"] != 1 {
		t.Fatal("input alias survived")
	}
	v, err := wireNumbers([]any{json.Number("9007199254740993"), json.Number("-0"), json.Number("1.25")})
	if err != nil {
		t.Fatal(err)
	}
	items := v.([]any)
	if items[0] != int64(9007199254740993) || !math.Signbit(items[1].(float64)) || items[2] != 1.25 {
		t.Fatal(items)
	}
}
