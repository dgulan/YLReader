package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestFollowupWeakProductionJWTLogsBeforeDatabase(t *testing.T) {
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("JWT_SECRET", "private-short-key")
	t.Setenv("LOCAL_IMPORT_TIMEOUT", "")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prior := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = prior; reader.Close(); writer.Close() }()
	err = run()
	writer.Close()
	data, readErr := io.ReadAll(reader)
	if err == nil || readErr != nil || strings.Contains(string(data), "private-short-key") {
		t.Fatal("missing failure or leaked secret", err, readErr)
	}
	var entry map[string]any
	if json.Unmarshal(data, &entry) != nil || entry["msg"] != "invalid JWT_SECRET" || entry["level"] != "ERROR" {
		t.Fatal("missing structured diagnostic", string(data))
	}
}
