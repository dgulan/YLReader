//go:build integration && linux

package backup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestRestoreHeaderIntegration(t *testing.T) {
	raw := os.Getenv("BACKUP_RESTORE_DSN")
	sourceRaw := os.Getenv("BACKUP_HEADER_SOURCE_DSN")
	if raw == "" || sourceRaw == "" {
		t.Skip("fresh isolated cluster and synthetic header source required")
	}
	cleanPG(t)
	ctx := context.Background()
	tool := PostgresArchiveTool{Executable: os.Getenv("BACKUP_PG_RESTORE")}
	sourceConfig, e := ParseTarget(sourceRaw)
	if e != nil || sourceConfig.Host != "127.0.0.1" || sourceConfig.Port != 5432 || sourceConfig.User != "postgres" || sourceConfig.Database != "yreader_a1_iso_header_source" {
		t.Fatal("dedicated synthetic source required")
	}
	source, e := pgx.ConnectConfig(ctx, sourceConfig)
	if e != nil {
		t.Fatal("synthetic source unavailable")
	}
	defer source.Close(ctx)
	specs := []struct {
		name    string
		failure error
		outcome string
	}{{"headergood", nil, "committed"}, {"headerpubfail", nil, "committed"}, {"headerpuback", nil, "committed"}, {"headerpubintent", nil, "committed"}, {"headerpubcopy", nil, "committed"}, {"headerpubrename", nil, "committed"}, {"headerwrong", ErrHeaderDecrypt, "rolled_back"}, {"headermissing", ErrHeaderKey, "not_started"}, {"headeraad", ErrHeaderDecrypt, "rolled_back"}, {"headerinventory", ErrHeaderInventory, "rolled_back"}, {"headerplain", ErrHeaderEnvelope, "rolled_back"}, {"headerhttp", nil, "committed"}}
	if os.Getenv("BACKUP_BEHAVIOR_DISPATCH") == "1" {
		for _, name := range []string{"dispatch_good", "dispatch_lost", "dispatch_intent_ack", "dispatch_response_ack"} {
			specs = append(specs, struct {
				name    string
				failure error
				outcome string
			}{name, nil, "committed"})
		}
	}
	if os.Getenv("BACKUP_BEHAVIOR_HTTP") == "1" {
		for _, name := range []string{"dispatch_http_good", "dispatch_http_lost", "dispatch_http_redirect", "dispatch_http_wrong_pin"} {
			specs = append(specs, struct {
				name    string
				failure error
				outcome string
			}{name, nil, "committed"})
		}
	}
	for _, spec := range specs {
		t.Run(spec.name, func(t *testing.T) {
			var credentials *httpFixtureCredentials
			if spec.name == "headerhttp" || strings.HasPrefix(spec.name, "dispatch_") {
				credentials = seedHTTPFixture(t, sourceRaw)
			}
			for i := 1; i <= 2; i++ {
				user := uuid.MustParse("00000000-0000-4000-8000-00000000000" + string(rune('0'+i)))
				src := uuid.MustParse("10000000-0000-4000-8000-00000000000" + string(rune('0'+i)))
				aadUser := user
				if spec.name == "headeraad" && i == 1 {
					aadUser = uuid.New()
				}
				kid, value := "primary", byte(1)
				if i == 2 {
					kid, value = "legacy", 2
				}
				header := headerEnvelope(t, headerRing(t, kid, value), aadUser, src, []byte(`{"Authorization":"Bearer synthetic-private-token-中文😀"}`))
				if spec.name == "headerplain" && i == 1 {
					header = []byte(`{"Cookie":"synthetic-private-token"}`)
				}
				if _, e := source.Exec(ctx, `UPDATE book_sources SET header=$1::jsonb WHERE id=$2`, string(header), src); e != nil {
					t.Fatal("synthetic encryption fixture")
				}
			}
			binary := os.Getenv("BACKUP_PG_DUMP")
			if !filepath.IsAbs(binary) {
				t.Fatal("trusted fixture pg_dump required")
			}
			dumpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			command := exec.CommandContext(dumpCtx, binary, "-Fc", "-h", "127.0.0.1", "-p", "5432", "-U", "postgres", "-d", "yreader_a1_iso_header_source")
			command.Env = []string{"LC_ALL=C", "LANG=C"}
			output := &archiveOutput{remaining: 1 << 20}
			command.Stdout = output
			if e = command.Run(); e != nil {
				t.Fatal("synthetic header dump failed")
			}
			dumpRoot := t.TempDir()
			if e = os.Chmod(dumpRoot, 0700); e != nil {
				t.Fatal(e)
			}
			dumpFile := filepath.Join(dumpRoot, "database.dump")
			if e = os.WriteFile(dumpFile, output.Bytes(), 0600); e != nil {
				t.Fatal(e)
			}
			c := localConfig(t)
			u, _ := url.Parse(raw)
			u.Path = "/yreader_a1_iso_" + spec.name
			if e = os.WriteFile(c.Target.DSNFile, []byte(u.String()), 0600); e != nil {
				t.Fatal(e)
			}
			kids := []string{"legacy", "primary"}
			if spec.name == "headerinventory" {
				kids = []string{"primary"}
			}
			pkg := restoreFixturePackageHeaderAt(t, dumpFile, 1, c.Temporary.Root, kids)
			c.Source.InstanceID = pkg.Binding().SourceID
			c.Limits = pkg.verificationLimits
			keys := map[string]string{"primary": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), "legacy": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))}
			if spec.name == "headerwrong" {
				keys["primary"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
			}
			if spec.name == "headermissing" {
				delete(keys, "legacy")
			}
			keyJSON, _ := json.Marshal(map[string]any{"keyring_format": "ylreader-header-keyring", "keyring_version": 1, "current_kid": "primary", "keys": keys})
			keyFile := filepath.Join(c.Target.Storage.Root, "header-keys.json")
			if e = os.WriteFile(keyFile, keyJSON, 0600); e != nil {
				t.Fatal(e)
			}
			ring, e := LoadRestoreHeaderKeyring(ctx, keyFile)
			if e != nil {
				t.Fatal(e)
			}
			now := time.Now().UTC().Truncate(time.Microsecond)
			inspector := PostgresTargetInspector{}
			p, e := PreparePlan(ctx, c, pkg.Binding(), "", now, inspector)
			if e != nil {
				t.Fatalf("PreparePlan: %v", e)
			}
			g, e := acquireRestoreGuard(ctx, p, c, pkg, c.Target.InstanceID, now, inspector)
			if e != nil {
				t.Fatalf("acquireRestoreGuard: %v", e)
			}
			defer g.Close()
			owner := "50000000-0000-4000-8000-000000000001"
			stage, _, e := g.StageRecorded(ctx, pkg, owner, c.Target.InstanceID, now, inspector)
			if e != nil {
				t.Fatalf("StageRecorded: %v", e)
			}
			defer stage.Close()
			result, runErr := g.RunDatabaseWithHeaders(ctx, stage, owner, c.Target.InstanceID, now, tool, 1<<20, ring)
			if spec.failure == nil {
				if runErr != nil || !result.HeadersChecked || result.Operation.Phase != "validating" {
					t.Fatal("header-checked commit", runErr)
				}
			} else if !errors.Is(runErr, spec.failure) || result.HeadersChecked {
				t.Fatal("unsafe header accepted", runErr)
			}
			a, _, e := (attemptStore{Files: g.locations.files[3]}).Load(ctx, p.OperationID)
			if e != nil || a.DatabaseOutcome != spec.outcome {
				t.Fatal("header outcome", a.DatabaseOutcome, e)
			}
			observation, e := observeRecordedDatabase(ctx, u.String(), pkg.manifest.Counts)
			if e != nil {
				t.Fatal(e)
			}
			if spec.outcome == "committed" {
				if observation.State != "populated" || !observation.CountsMatch || stage.Reference() == "" {
					t.Fatal("checked data not retained")
				}
				replayed, e := g.RunDatabaseWithHeaders(ctx, stage, owner, c.Target.InstanceID, now.Add(time.Hour), tool, 1<<20, ring)
				if e != nil || replayed.Executed || replayed.HeadersChecked {
					t.Fatal("replay granted header proof", e)
				}
				mode := ""
				switch spec.name {
				case "headerpubfail":
					mode = "fail"
				case "headerpuback":
					mode = "ack"
				case "headerpubintent":
					mode = "intent"
				case "headerpubcopy":
					mode = "copying"
				case "headerpubrename":
					mode = "publishing"
				}
				assertPublicationIntegration(t, g, stage, c, p, owner, u.String(), ring, mode)
				if spec.name == "headergood" && os.Getenv("BACKUP_HANDOVER_DATA") == "1" {
					assertHandoverDataReview(t, g, stage, c, p, owner, u.String(), ring, source)
				}
				if credentials != nil {
					if strings.HasPrefix(spec.name, "dispatch_http_") {
						assertBehaviorHTTPDispatch(t, g, stage, p, owner, u.String(), strings.TrimPrefix(spec.name, "dispatch_http_"), ring, source, credentials)
					} else if strings.HasPrefix(spec.name, "dispatch_") {
						assertBehaviorDispatch(t, g, stage, p, owner, u.String(), strings.TrimPrefix(spec.name, "dispatch_"), ring, source, credentials)
					} else {
						assertRestoreHTTP(t, g, stage, owner, source, c, p, u.String(), ring, credentials)
					}
				}
			} else {
				if observation.State != "pristine" {
					t.Fatal("bad header committed", observation)
				}
				if spec.outcome == "rolled_back" {
					if empty, e := g.locations.files[1].Empty(ctx); e != nil || !empty {
						t.Fatal("header rollback object residue", e)
					}
				}
				if spec.outcome == "not_started" && stage.Reference() == "" {
					t.Fatal("preflight rejection deleted stage")
				}
			}
		})
	}
}
