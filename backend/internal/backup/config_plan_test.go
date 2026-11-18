package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"yreader/backend/internal/pkg/storage"
)

func configExample(t *testing.T) Config {
	t.Helper()
	data, e := os.ReadFile("../../../docs/examples/backup-config.json")
	if e != nil {
		t.Fatal(e)
	}
	c, e := LoadConfig(bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func planExample(t *testing.T) Plan {
	t.Helper()
	data, e := os.ReadFile("../../../docs/examples/restore-plan.json")
	if e != nil {
		t.Fatal(e)
	}
	p, e := LoadPlan(bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestStrictConfigExampleAndRejections(t *testing.T) {
	c := configExample(t)
	base, _ := json.Marshal(c)
	cases := []string{
		string(base) + "{}", strings.Replace(string(base), `"config_version":1`, `"config_version":1,"config_version":1`, 1),
		strings.Replace(string(base), `"provider":"localfs"`, `"provider":"localfs","provider":"localfs"`, 1),
		strings.Replace(string(base), `"config_version":1`, `"Config_Version":1`, 1), strings.Replace(string(base), `"config_version":1`, `"config_version":null`, 1),
		strings.Replace(string(base), `"provider":"localfs"`, `"provider":"s3"`, 1), strings.Replace(string(base), `"config_version":1`, `"config_version":1,"password":"synthetic"`, 1),
		strings.Replace(string(base), `"max_objects":1000000`, `"max_objects":1e6`, 1),
		strings.Replace(string(base), `"max_objects":1000000`, `"max_objects":-1`, 1),
		strings.Replace(string(base), `"namespace":"backups"`, `"namespace":"source/nested"`, 1),
		strings.Replace(string(base), `"namespace":"backups"`, `"namespace":"SOURCE"`, 1),
		strings.Replace(string(base), `"recipient_file":"D:/isolated-backup-example/recipient.txt"`, `"recipient_file":""`, 1),
		strings.Replace(string(base), `"namespace":"backups"`, `"namespace":"../escape"`, 1),
	}
	for i, data := range cases {
		if _, e := LoadConfig(strings.NewReader(data)); !errors.Is(e, ErrConfig) || strings.Contains(e.Error(), "synthetic") {
			t.Fatalf("case %d accepted or leaked: %v", i, e)
		}
	}
	if _, e := LoadConfig(bytes.NewReader(append(base, 0xff))); e == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, e := LoadConfig(strings.NewReader(strings.Repeat(" ", (1<<20)+1))); e == nil {
		t.Fatal("oversized config accepted")
	}
}

func TestPlanDigestMatchesFrozenPythonFixture(t *testing.T) {
	p := planExample(t)
	if p.Digest != "14264badef24a72073515503de99a6c1df94ff373497a1244ab2c5ae61f83fe8" {
		t.Fatal("digest compatibility changed")
	}
	// Re-order input keys, not the contract's canonical representation.
	data, _ := json.Marshal(p)
	var values map[string]json.RawMessage
	json.Unmarshal(data, &values)
	data, _ = json.Marshal(values)
	if _, e := LoadPlan(bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
	p.Database.Port++
	if p.Validate() == nil {
		t.Fatal("tampered target accepted")
	}
}

func TestPlanStrictFormat(t *testing.T) {
	p := planExample(t)
	data, _ := json.Marshal(p)
	for _, bad := range []string{string(data) + "{}", strings.Replace(string(data), `"plan_version":1`, `"plan_version":1,"plan_version":1`, 1), strings.Replace(string(data), `"plan_version":1`, `"plan_version":1,"secret":"synthetic"`, 1), strings.Replace(string(data), `"plan_version":1`, `"plan_version":null`, 1), strings.Replace(string(data), "08:00:00Z", "08:00:00+00:00", 1)} {
		if _, e := LoadPlan(strings.NewReader(bad)); !errors.Is(e, ErrPlan) {
			t.Fatal("malformed plan accepted", e)
		}
	}
	expired := p
	created, _ := time.Parse(time.RFC3339Nano, expired.Created)
	expired.Expires = created.Add(16 * time.Minute).Format(time.RFC3339Nano)
	if expired.Validate() == nil {
		t.Fatal("invalid lifetime accepted")
	}
	badLimits := p
	badLimits.Limits.Objects = -1
	if badLimits.Validate() == nil {
		t.Fatal("negative limit accepted")
	}
}

func TestPlanFractionalTimestampCanonicalBytes(t *testing.T) {
	p := planExample(t)
	p.Created = "2026-11-13T08:00:00.000000Z"
	p.Expires = "2026-11-13T08:15:00.000000Z"
	// Independently computed using Python's frozen canonicalization algorithm.
	p.Digest = "e66974820c996447b9c83ba1f8cb5a18ed398d8405b02c5d6c986b7d2ac56768"
	data, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = LoadPlan(bytes.NewReader(data)); e != nil {
		t.Fatal("fractional zeros changed canonical bytes", e)
	}
}

func TestPhysicalLocationModel(t *testing.T) {
	a := storage.FileIdentity{Device: 1, Inode: 2, MountID: 3}
	b := storage.FileIdentity{Device: 1, Inode: 4, MountID: 3}
	parent := storage.FileIdentity{Device: 1, Inode: 1, MountID: 3}
	good := []storage.DirectoryLocation{{Identity: a, Ancestors: []storage.FileIdentity{a, parent}}, {Identity: b, Ancestors: []storage.FileIdentity{b, parent}}}
	if e := disjoint(good); e != nil {
		t.Fatal(e)
	}
	for _, bad := range [][]storage.DirectoryLocation{
		{good[0], good[0]},
		{good[0], {Identity: parent, Ancestors: []storage.FileIdentity{parent}}},
		{good[0], {Identity: storage.FileIdentity{Device: 1, Inode: 99, MountID: 4}}},
	} {
		if disjoint(bad) == nil {
			t.Fatal("possible alias accepted")
		}
	}
	if fingerprint(good[0], "target") == fingerprint(good[0], "control") {
		t.Fatal("namespace not bound")
	}
	if !validStamp(time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("test clock invalid")
	}
}
