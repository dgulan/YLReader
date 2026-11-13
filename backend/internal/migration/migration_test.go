package migration

import (
	"os"
	"strings"
	"testing"
)

func TestBaselineMatchesAuthoritativeSchema(t *testing.T) {
	data, err := os.ReadFile("../../../docs/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	start := strings.Index(text, "-- ADR-017 baseline registration")
	end := strings.LastIndex(text, "COMMIT;")
	if start < 0 || end < start {
		t.Fatal("missing registration")
	}
	registration := text[start:end]
	if !strings.Contains(registration, Expected().Checksum) {
		t.Fatal("registration checksum drift")
	}
	text = text[:start] + text[end:]
	text = strings.Replace(text, "\nBEGIN;\n", "\n", 1)
	end = strings.LastIndex(text, "COMMIT;")
	text = text[:end] + text[end+len("COMMIT;"):]
	if strings.TrimSpace(text)+"\n" != baseline {
		t.Fatal("embedded baseline drift")
	}
}
func TestRejectsUnknownOrAlteredLedger(t *testing.T) {
	e := Expected()
	if validate([]Entry{e}) != nil {
		t.Fatal("expected baseline")
	}
	e.Checksum = "bad"
	if validate([]Entry{e}) == nil {
		t.Fatal("accepted drift")
	}
	e = Expected()
	e.Version = 2
	if validate([]Entry{e}) == nil {
		t.Fatal("accepted future")
	}
}
