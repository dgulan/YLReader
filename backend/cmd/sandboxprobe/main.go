// sandboxprobe is an offline, one-request evaluation tool for isolated experiments.
// It is not a server worker and never receives an HTTP client or service secrets.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"yreader/backend/internal/engine/sandbox"
)

type request struct {
	Script     string         `json:"script"`
	Variables  map[string]any `json:"variables"`
	Structured bool           `json:"structured"`
}
type response struct {
	OK    bool   `json:"ok"`
	Value any    `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

func evaluate(input io.Reader, output io.Writer) error {
	data, err := io.ReadAll(io.LimitReader(input, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return json.NewEncoder(output).Encode(response{Error: "invalid_input"})
	}
	var req request
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return json.NewEncoder(output).Encode(response{Error: "invalid_input"})
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
		return json.NewEncoder(output).Encode(response{Error: "invalid_input"})
	}
	sb := sandbox.New(sandbox.Config{}, nil)
	var value any
	if req.Structured {
		value, err = sb.EvalValue(context.Background(), req.Script, req.Variables)
	} else {
		value, err = sb.Eval(context.Background(), req.Script, req.Variables)
	}
	if err != nil {
		code := "rule_execution_failed"
		switch {
		case errors.Is(err, sandbox.ErrSandboxTimeout):
			code = "sandbox_timeout"
		case errors.Is(err, sandbox.ErrSandboxOutputLimit):
			code = "sandbox_output_limit"
		case errors.Is(err, sandbox.ErrSandboxPanic):
			code = "sandbox_panic"
		}
		return json.NewEncoder(output).Encode(response{Error: code})
	}
	return json.NewEncoder(output).Encode(response{OK: true, Value: value})
}
func main() {
	if evaluate(os.Stdin, os.Stdout) != nil {
		os.Exit(1)
	}
}
