package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestProbeProtocol(t *testing.T) {
	for _, tc := range []struct {
		input string
		ok    bool
		error string
	}{
		{`{"script":"'ok'"}`, true, ""},
		{`{"script":"({items:[1,true]})","structured":true}`, true, ""},
		{`{"script":"java.ajax('http://127.0.0.1')"}`, false, "rule_execution_failed"},
		{`{"script":"1","secret":"not-a-protocol-field"}`, false, "invalid_input"},
		{`{"script":"1"} {}`, false, "invalid_input"},
		{`null`, false, "invalid_input"},
		{strings.Repeat("x", (4<<20)+1), false, "invalid_input"},
	} {
		var output bytes.Buffer
		if err := evaluate(strings.NewReader(tc.input), &output); err != nil {
			t.Fatal(err)
		}
		var reply response
		if err := json.Unmarshal(output.Bytes(), &reply); err != nil || reply.OK != tc.ok || reply.Error != tc.error {
			t.Fatal(output.String(), err)
		}
	}
}
