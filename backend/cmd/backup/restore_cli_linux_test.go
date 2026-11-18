//go:build integration && linux

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/backup"
	"yreader/backend/internal/migration"
	"yreader/backend/internal/pkg/cache"
	headercrypto "yreader/backend/internal/pkg/crypto"
)

func TestBackupRestoreCLIIsolated(t *testing.T) {
	cli := os.Getenv("BACKUP_RESTORE_CLI")
	pgRestore := os.Getenv("BACKUP_RESTORE_PG_RESTORE")
	pgDump := os.Getenv("BACKUP_RESTORE_PG_DUMP")
	srcDSN := os.Getenv("BACKUP_RESTORE_SOURCE_DSN")
	targetBase := os.Getenv("BACKUP_RESTORE_TARGET_BASE")
	srcRedisURL := os.Getenv("BACKUP_RESTORE_SOURCE_REDIS")
	dstRedisURL := os.Getenv("BACKUP_RESTORE_TARGET_REDIS")
	srcRedisID := os.Getenv("BACKUP_RESTORE_SOURCE_REDIS_ID")
	dstRedisID := os.Getenv("BACKUP_RESTORE_TARGET_REDIS_ID")
	if cli == "" {
		t.Skip("explicit isolated restore CLI container required")
	}
	if cli != "/verify/backup-cli" || pgRestore != "/usr/bin/pg_restore" || pgDump != "/usr/bin/pg_dump" ||
		!strings.HasPrefix(srcDSN, "postgres://postgres:@127.0.0.1:5432/") || targetBase != "postgres://reader:@127.0.0.1:5432" ||
		srcRedisURL != "redis://127.0.0.1:16379/0" || dstRedisURL != "redis://127.0.0.1:16380/0" ||
		len(srcRedisID) != 40 || len(dstRedisID) != 40 || os.Geteuid() != 10001 {
		t.Fatal("dedicated isolated restore CLI fixture required")
	}
	for _, entry := range os.Environ() {
		if k, _, _ := strings.Cut(entry, "="); strings.HasPrefix(strings.ToUpper(k), "PG") {
			_ = os.Unsetenv(k)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Seed source DB book_sources.header with legacy + primary keys and gather exact manifest counts/objects.
	ring, e := headercrypto.NewKeyring("primary", map[string][]byte{
		"primary": bytes.Repeat([]byte{1}, 32),
		"legacy":  bytes.Repeat([]byte{2}, 32),
	})
	if e != nil {
		t.Fatal(e)
	}
	srcCfg, e := backup.ParseTarget(srcDSN)
	if e != nil {
		t.Fatal(e)
	}
	srcDB, e := pgx.ConnectConfig(ctx, srcCfg)
	if e != nil {
		t.Fatal(e)
	}
	defer srcDB.Close(ctx)

	type srcRow struct{ id, user uuid.UUID }
	var srcRows []srcRow
	rows, e := srcDB.Query(ctx, `SELECT id, user_id FROM public.book_sources ORDER BY id`)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var r srcRow
		if rows.Scan(&r.id, &r.user) != nil {
			t.Fatal("scan book_sources")
		}
		srcRows = append(srcRows, r)
	}
	rows.Close()
	if len(srcRows) == 0 {
		t.Fatal("empty source fixture book_sources")
	}
	for i, r := range srcRows {
		useRing := ring
		if i%2 == 1 {
			useRing, _ = headercrypto.NewKeyring("legacy", map[string][]byte{"legacy": bytes.Repeat([]byte{2}, 32)})
		}
		sealed, e := useRing.Seal([]byte(`{"Authorization":"Bearer synthetic-cli-token"}`), headercrypto.AAD(r.user, r.id))
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(map[string]string{"__enc": sealed})
		if _, e := srcDB.Exec(ctx, `UPDATE public.book_sources SET header=$1::jsonb WHERE id=$2`, string(raw), r.id); e != nil {
			t.Fatal(e)
		}
	}

	var srcPin, dstCorePin, dstPartialPin struct {
		sys        string
		dbOID, rID uint32
	}
	if srcDB.QueryRow(ctx, `SELECT (SELECT system_identifier::text FROM pg_catalog.pg_control_system()),
 (SELECT oid FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database()),
 (SELECT oid FROM pg_catalog.pg_roles WHERE rolname='reader')`).Scan(&srcPin.sys, &srcPin.dbOID, &srcPin.rID) != nil {
		t.Fatal("source pin query failed")
	}
	dstCorePin.sys, dstCorePin.rID = srcPin.sys, srcPin.rID
	dstPartialPin.sys, dstPartialPin.rID = srcPin.sys, srcPin.rID
	if srcDB.QueryRow(ctx, `SELECT (SELECT oid FROM pg_catalog.pg_database WHERE datname='yreader_a1_iso_restore_core'),
 (SELECT oid FROM pg_catalog.pg_database WHERE datname='yreader_a1_iso_restore_partial')`).Scan(&dstCorePin.dbOID, &dstPartialPin.dbOID) != nil {
		t.Fatal("target database OID query failed")
	}

	var counts backup.ManifestCounts
	if srcDB.QueryRow(ctx, `SELECT
 (SELECT COUNT(*) FROM schema_migrations),
 (SELECT COUNT(*) FROM users),
 (SELECT COUNT(*) FROM auth_sessions),
 (SELECT COUNT(*) FROM app_settings),
 (SELECT COUNT(*) FROM book_sources),
 (SELECT COUNT(*) FROM source_failures),
 (SELECT COUNT(*) FROM books),
 (SELECT COUNT(*) FROM book_source_mappings),
 (SELECT COUNT(*) FROM chapters),
 (SELECT COUNT(*) FROM reading_progress),
 (SELECT COUNT(*) FROM chapter_source_overrides),
 (SELECT COUNT(*) FROM bookmarks),
 (SELECT COUNT(*) FROM replace_rules),
 (SELECT COUNT(*) FROM jobs)`).Scan(
		&counts.Migrations, &counts.Users, &counts.Sessions, &counts.Settings,
		&counts.Sources, &counts.Failures, &counts.Books, &counts.Mappings,
		&counts.Chapters, &counts.Progress, &counts.Overrides, &counts.Bookmarks,
		&counts.Rules, &counts.Jobs) != nil {
		t.Fatal("source table counts failed")
	}

	var objectKeys []string
	objRows, e := srcDB.Query(ctx, `SELECT b.id::text, c.chapter_index FROM books b JOIN chapters c ON c.book_id=b.id WHERE b.book_type='local' AND NOT c.is_volume ORDER BY b.id, c.chapter_index`)
	if e != nil {
		t.Fatal(e)
	}
	for objRows.Next() {
		var book string
		var idx int32
		if objRows.Scan(&book, &idx) != nil {
			t.Fatal("scan local chapters")
		}
		objectKeys = append(objectKeys, fmt.Sprintf("local/%s/%d.txt", book, idx))
	}
	objRows.Close()

	dumpCmd := exec.CommandContext(ctx, pgDump, "-Fc", "-h", "127.0.0.1", "-p", "5432", "-U", "postgres", "-d", "yreader_a1_iso_restore_src")
	dumpCmd.Env = []string{"LC_ALL=C", "LANG=C"}
	dumpBytes, e := dumpCmd.Output()
	if e != nil {
		t.Fatal("pg_dump fixture failed", e)
	}

	sha := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	backupID := "30000000-0000-4000-8000-000000000001"
	sourceInstanceID := "10000000-0000-4000-8000-000000000001"
	ownerID := "50000000-0000-4000-8000-000000000001"
	exp := migration.Expected()
	manifest := backup.Manifest{
		Format:        "ylreader-instance-backup",
		Version:       1,
		BackupID:      backupID,
		SourceID:      sourceInstanceID,
		Created:       time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano),
		Snapshot:      time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond).Format(time.RFC3339Nano),
		Revision:      strings.Repeat("a", 40),
		SchemaVersion: int(exp.Version),
		Migrations:    []backup.ManifestMigration{{Version: int(exp.Version), Name: exp.Name, Checksum: exp.Checksum}},
		PostgresMajor: 16,
		HeaderKIDs:    []string{"legacy", "primary"},
		Database:      backup.ArchiveFile{Name: "database.dump", Bytes: int64(len(dumpBytes)), SHA: sha(dumpBytes)},
		Objects:       []backup.ManifestObject{},
		Counts:        counts,
		TotalBytes:    int64(len(dumpBytes)),
	}
	type tarPart struct {
		name string
		body []byte
	}
	parts := []tarPart{{"database.dump", dumpBytes}}
	for i, key := range objectKeys {
		body := []byte(fmt.Sprintf("章节内容 #%d\n", i))
		oid := sha([]byte(key))[:32]
		manifest.Objects = append(manifest.Objects, backup.ManifestObject{ID: oid, Key: key, Bytes: int64(len(body)), SHA: sha(body)})
		manifest.TotalBytes += int64(len(body))
		parts = append(parts, tarPart{"objects/" + oid, body})
	}
	manifest.ObjectCount = int64(len(manifest.Objects))
	rawManifest, e := json.Marshal(manifest)
	if e != nil {
		t.Fatal(e)
	}
	allParts := append([]tarPart{{"manifest.json", rawManifest}}, parts...)
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	for _, part := range allParts {
		if tw.WriteHeader(&tar.Header{Name: part.name, Size: int64(len(part.body)), Mode: 0600, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}) != nil {
			t.Fatal("tar header")
		}
		if len(part.body) > 0 {
			if _, e := tw.Write(part.body); e != nil {
				t.Fatal("tar write")
			}
		}
	}
	if tw.Close() != nil {
		t.Fatal("tar close")
	}
	id, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	var cipherBuf bytes.Buffer
	aw, e := age.Encrypt(&cipherBuf, id.Recipient())
	if e != nil {
		t.Fatal(e)
	}
	if _, e := aw.Write(tarBuf.Bytes()); e != nil || aw.Close() != nil {
		t.Fatal("age encrypt")
	}
	cipherBytes := cipherBuf.Bytes()

	limits := backup.Limits{Ciphertext: 8 << 20, Plaintext: 16 << 20, Manifest: 256 << 10, Objects: 100, Object: 1 << 20}
	req := backup.InspectionRequest{Format: "ylreader-package-inspection-request", Version: 1, PackageKey: "package.age", IdentityKey: "identity.txt", BackupID: backupID, Bytes: int64(len(cipherBytes)), SHA: sha(cipherBytes), HeaderKIDs: []string{"legacy", "primary"}, Limits: limits}

	runCLI := func(args ...string) ([]byte, []byte, int) {
		t.Helper()
		cmd := exec.CommandContext(ctx, cli, args...)
		cmd.Env = []string{"LC_ALL=C", "LANG=C"}
		var out, errout bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errout
		e := cmd.Run()
		code := 0
		if e != nil {
			exit, ok := e.(*exec.ExitError)
			if !ok {
				t.Fatalf("subprocess failed: %v", e)
			}
			code = exit.ExitCode()
		}
		return out.Bytes(), errout.Bytes(), code
	}

	type restoreEnv struct {
		root, inputDir, controlDir, targetDSN, hdrPath, wrongHdrPath, dbName string
		cfg                                                                  backup.Config
		plan                                                                 backup.Plan
	}
	newEnv := func(suffix string) restoreEnv {
		base := t.TempDir()
		_ = os.Chmod(base, 0700)
		mk := func(rel string) string {
			p := filepath.Join(base, rel)
			if os.MkdirAll(p, 0700) != nil || os.Chmod(p, 0700) != nil {
				t.Fatal("mkdir", p)
			}
			return p
		}
		in := mk("in")
		srcSto := mk("srcsto")
		dstSto := mk("dststo")
		bakSto := mk("baksto")
		ctrlSto := mk("ctrlsto")
		mk("srcsto/ns")
		mk("dststo/ns")
		mk("baksto/ns")
		mk("ctrlsto/ns")
		tmpSto := mk("tmpsto")
		secDir := mk("secrets")
		writeFile := func(path string, b []byte) {
			if os.WriteFile(path, b, 0600) != nil {
				t.Fatal("write", path)
			}
		}
		encode := func(v any) []byte { b, _ := json.Marshal(v); return b }

		dbName := "yreader_a1_iso_restore_" + suffix
		u, _ := url.Parse(targetBase + "/" + dbName + "?sslmode=disable")
		srcDSNFile := filepath.Join(secDir, "source.dsn")
		dstDSNFile := filepath.Join(secDir, "target.dsn")
		writeFile(srcDSNFile, []byte(srcDSN))
		writeFile(dstDSNFile, []byte(u.String()))
		hdrPath := filepath.Join(secDir, "header-keys.json")
		wrongHdrPath := filepath.Join(secDir, "wrong-header-keys.json")
		writeFile(hdrPath, encode(map[string]any{
			"keyring_format": "ylreader-header-keyring", "keyring_version": 1, "current_kid": "primary",
			"keys": map[string]string{
				"primary": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
				"legacy":  base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)),
			},
		}))
		writeFile(wrongHdrPath, encode(map[string]any{
			"keyring_format": "ylreader-header-keyring", "keyring_version": 1, "current_kid": "primary",
			"keys": map[string]string{
				"primary": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)),
				"legacy":  base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)),
			},
		}))

		writeFile(filepath.Join(in, "package.age"), cipherBytes)
		writeFile(filepath.Join(in, "identity.txt"), []byte(id.String()))
		writeFile(filepath.Join(in, "request.json"), encode(req))

		var cfg backup.Config
		cfg.Format = "ylreader-backup-localfs-experiment"
		cfg.Version = 1
		cfg.Source = backup.Endpoint{InstanceID: sourceInstanceID, DSNFile: srcDSNFile, Storage: backup.LocalStorage{Provider: "localfs", Root: srcSto, Namespace: "ns"}}
		cfg.Target = backup.Endpoint{InstanceID: uuid.NewString(), DSNFile: dstDSNFile, Storage: backup.LocalStorage{Provider: "localfs", Root: dstSto, Namespace: "ns"}}
		cfg.Backup = backup.LocalStorage{Provider: "localfs", Root: bakSto, Namespace: "ns"}
		cfg.Control = backup.LocalStorage{Provider: "localfs", Root: ctrlSto, Namespace: "ns"}
		cfg.Temporary.Root = tmpSto
		cfg.Encryption.Identity = filepath.Join(in, "identity.txt")
		cfg.QuiescenceFile = filepath.Join(secDir, "quiesce")
		cfg.Limits = limits
		writeFile(filepath.Join(in, "config.json"), encode(cfg))

		// Build frozen candidate plan strictly using the read-only `plan` CLI command.
		out, errout, code := runCLI("plan", "--input-root", in, "--request-key", "request.json", "--config-key", "config.json", "--format", "json")
		if code != 0 {
			t.Fatalf("plan preview failed (%s): code=%d err=%s", suffix, code, errout)
		}
		var preview struct {
			Plan       backup.Plan `json:"plan"`
			Execution  bool        `json:"execution_authorized"`
			Deployment bool        `json:"deployment_authorized"`
		}
		if json.Unmarshal(out, &preview) != nil || preview.Execution || preview.Deployment || preview.Plan.Validate() != nil {
			t.Fatalf("invalid preview output: %s", out)
		}
		writeFile(filepath.Join(in, "plan.json"), encode(preview.Plan))
		return restoreEnv{base, in, filepath.Join(ctrlSto, "ns"), u.String(), hdrPath, wrongHdrPath, dbName, cfg, preview.Plan}
	}

	assertScratchDiskState := func(t *testing.T, env restoreEnv, wantPresent bool, wantOutcome string) {
		t.Helper()
		rawAttempt, err := os.ReadFile(filepath.Join(env.controlDir, "operations", env.plan.OperationID, "attempt.json"))
		if err != nil {
			t.Fatalf("read attempt.json: %v", err)
		}
		var att struct {
			DatabaseOutcome string  `json:"database_outcome"`
			Generation      *string `json:"generation_reference"`
		}
		if err := json.Unmarshal(rawAttempt, &att); err != nil {
			t.Fatalf("decode attempt.json: %v", err)
		}
		if att.DatabaseOutcome != wantOutcome {
			t.Fatalf("attempt database_outcome=%q want %q", att.DatabaseOutcome, wantOutcome)
		}
		targetNS := filepath.Join(env.cfg.Target.Storage.Root, env.cfg.Target.Storage.Namespace)
		if !wantPresent {
			entries, err := os.ReadDir(targetNS)
			if err != nil {
				t.Fatalf("readdir %s: %v", targetNS, err)
			}
			for _, ent := range entries {
				if strings.HasPrefix(ent.Name(), ".scratch-") {
					t.Fatalf("expected rollback cleanup to remove %s", ent.Name())
				}
			}
			return
		}
		if att.Generation == nil || !strings.HasPrefix(*att.Generation, ".scratch-") {
			t.Fatalf("expected attempt.json.generation_reference .scratch-* when retained, got %v", att.Generation)
		}
		scratchDir := filepath.Join(targetNS, *att.Generation)
		info, err := os.Stat(scratchDir)
		if err != nil || !info.IsDir() {
			t.Fatalf("expected retained scratch dir %s on disk: err=%v", scratchDir, err)
		}
		if _, err := os.Stat(filepath.Join(scratchDir, "restore-owner.json")); err != nil {
			t.Fatalf("expected restore-owner.json in retained %s: %v", scratchDir, err)
		}
		for _, obj := range manifest.Objects {
			objPath := filepath.Join(scratchDir, "object-"+obj.ID)
			fi, err := os.Stat(objPath)
			if err != nil || fi.Size() != obj.Bytes {
				t.Fatalf("expected staged object %s (%d bytes) intact in %s: err=%v info=%v", obj.ID, obj.Bytes, scratchDir, err, fi)
			}
		}
	}

	assertNoSecrets := func(env restoreEnv, combined string) {
		t.Helper()
		for _, secret := range []string{id.String(), env.targetDSN, env.inputDir, "Authorization"} {
			if strings.Contains(combined, secret) {
				t.Fatalf("secret leaked in CLI output: %q in %s", secret, combined)
			}
		}
	}
	assertTargetEmpty := func(dsn string) {
		t.Helper()
		tc, e := backup.ParseTarget(dsn)
		if e != nil {
			t.Fatal(e)
		}
		if (backup.PostgresTargetInspector{}).CheckEmpty(ctx, tc) != nil {
			t.Fatal("target database was mutated")
		}
	}

	core := newEnv("core")
	t.Run("reject_preview_flag_escalation", func(t *testing.T) {
		out, _, code := runCLI("plan", "--input-root", core.inputDir, "--request-key", "request.json", "--config-key", "config.json", "--plan-digest", core.plan.Digest)
		if code != 2 || len(out) != 0 {
			t.Fatalf("preview accepted restore flag: code=%d out=%s", code, out)
		}
	})
	t.Run("reject_missing_authorization_flags", func(t *testing.T) {
		out, _, code := runCLI("restore", "--input-root", core.inputDir, "--request-key", "request.json", "--config-key", "config.json", "--plan-key", "plan.json", "--plan-digest", core.plan.Digest)
		if code != 2 || len(out) != 0 {
			t.Fatalf("incomplete restore args accepted: code=%d out=%s", code, out)
		}
		assertTargetEmpty(core.targetDSN)
	})
	t.Run("reject_mismatched_plan_digest", func(t *testing.T) {
		badDigest := strings.Repeat("0", 64)
		if core.plan.Digest == badDigest {
			badDigest = strings.Repeat("1", 64)
		}
		out, errout, code := runCLI("restore", "--input-root", core.inputDir, "--request-key", "request.json", "--config-key", "config.json", "--plan-key", "plan.json", "--plan-digest", badDigest, "--confirm-target", core.cfg.Target.InstanceID, "--owner", ownerID, "--header-keys", core.hdrPath, "--pg-restore", pgRestore, "--timeout", "2m")
		var res backup.ControlledRestoreResult
		if code != 4 || json.Unmarshal(out, &res) != nil || res.StatusCode != "RESTORE_REJECTED" || res.RetrySafe || res.ExecutionAuthorized || res.ReadyForCutover || res.DeploymentAuthorized {
			t.Fatalf("mismatched digest not rejected cleanly: code=%d out=%s err=%s", code, out, errout)
		}
		assertNoSecrets(core, string(out)+string(errout))
		assertTargetEmpty(core.targetDSN)
	})
	t.Run("reject_occupied_target", func(t *testing.T) {
		tc, _ := backup.ParseTarget(core.targetDSN)
		db, e := pgx.ConnectConfig(ctx, tc)
		if e != nil {
			t.Fatal(e)
		}
		if _, e := db.Exec(ctx, `CREATE TABLE restore_cli_occupied(id integer)`); e != nil {
			db.Close(ctx)
			t.Fatal(e)
		}
		db.Close(ctx)
		out, errout, code := runCLI("restore", "--input-root", core.inputDir, "--request-key", "request.json", "--config-key", "config.json", "--plan-key", "plan.json", "--plan-digest", core.plan.Digest, "--confirm-target", core.cfg.Target.InstanceID, "--owner", ownerID, "--header-keys", core.hdrPath, "--pg-restore", pgRestore, "--timeout", "2m")
		db, _ = pgx.ConnectConfig(ctx, tc)
		_, _ = db.Exec(ctx, `DROP TABLE restore_cli_occupied`)
		db.Close(ctx)
		var res backup.ControlledRestoreResult
		if code != 4 || json.Unmarshal(out, &res) != nil || res.StatusCode != "RESTORE_REJECTED" {
			t.Fatalf("occupied target accepted: code=%d out=%s err=%s", code, out, errout)
		}
		assertNoSecrets(core, string(out)+string(errout))
	})

	failEnv := newEnv("fail")
	t.Run("reject_wrong_header_keyring_rollback", func(t *testing.T) {
		out, errout, code := runCLI("restore", "--input-root", failEnv.inputDir, "--request-key", "request.json", "--config-key", "config.json", "--plan-key", "plan.json", "--plan-digest", failEnv.plan.Digest, "--confirm-target", failEnv.cfg.Target.InstanceID, "--owner", ownerID, "--header-keys", failEnv.wrongHdrPath, "--pg-restore", pgRestore, "--timeout", "2m")
		var res backup.ControlledRestoreResult
		if code != 4 || json.Unmarshal(out, &res) != nil || res.StatusCode != "RESTORE_REJECTED" || res.DatabaseOutcome != "rolled_back" || res.HeadersChecked || res.RetrySafe {
			t.Fatalf("wrong keyring not rolled back cleanly: code=%d out=%s err=%s", code, out, errout)
		}
		assertNoSecrets(failEnv, string(out)+string(errout))
		assertTargetEmpty(failEnv.targetDSN)
		assertScratchDiskState(t, failEnv, false, "rolled_back")
	})

	t.Run("controlled_restore_core_published_and_no_replay", func(t *testing.T) {
		out, errout, code := runCLI("restore", "--input-root", core.inputDir, "--request-key", "request.json", "--config-key", "config.json", "--plan-key", "plan.json", "--plan-digest", core.plan.Digest, "--confirm-target", core.cfg.Target.InstanceID, "--owner", ownerID, "--header-keys", core.hdrPath, "--pg-restore", pgRestore, "--timeout", "2m")
		var res backup.ControlledRestoreResult
		if code != 0 || json.Unmarshal(out, &res) != nil || res.StatusCode != "RESTORE_VALIDATING_PUBLISHED" || res.Phase != "validating" || res.DatabaseOutcome != "committed" || !res.HeadersChecked || res.PublicationState != "verified" || !res.ArtifactsRetained || res.RetrySafe || res.ExecutionAuthorized || res.ReadyForCutover || res.DeploymentAuthorized {
			t.Fatalf("core controlled restore failed: code=%d out=%s err=%s", code, out, errout)
		}
		assertScratchDiskState(t, core, true, "committed")
		assertNoSecrets(core, string(out)+string(errout))

		// Second attempt using same plan.json must be rejected without mutating DB or records.
		rOut, rErr, rCode := runCLI("restore", "--input-root", core.inputDir, "--request-key", "request.json", "--config-key", "config.json", "--plan-key", "plan.json", "--plan-digest", core.plan.Digest, "--confirm-target", core.cfg.Target.InstanceID, "--owner", ownerID, "--header-keys", core.hdrPath, "--pg-restore", pgRestore, "--timeout", "2m")
		var rRes backup.ControlledRestoreResult
		if rCode != 4 || json.Unmarshal(rOut, &rRes) != nil || rRes.RetrySafe || rRes.ExecutionAuthorized || rRes.ReadyForCutover || rRes.DeploymentAuthorized {
			t.Fatalf("replay attempt succeeded or unsafe: code=%d out=%s err=%s", rCode, rOut, rErr)
		}

		// Read-only `status` CLI observes validating/published state and never authorizes cutover.
		stOut, _, stCode := runCLI("status", "--control-root", core.controlDir, "--operation", core.plan.OperationID, "--format", "json")
		var st backup.RestoreStatus
		if stCode != 4 || json.Unmarshal(stOut, &st) != nil || st.Status != "records_only" || st.Reason != "report_absent" || st.Phase != "validating" || st.Persistence != "not_inferred" || st.Ready || st.Deployment {
			t.Fatalf("unexpected status after core restore: code=%d out=%s", stCode, stOut)
		}
	})

	partialEnv := newEnv("partial")
	t.Run("controlled_restore_partial_handover_no_registration", func(t *testing.T) {
		sw, tw := make([]byte, 32), make([]byte, 32)
		if _, e := rand.Read(sw); e != nil {
			t.Fatal(e)
		}
		if _, e := rand.Read(tw); e != nil {
			t.Fatal(e)
		}
		for _, pair := range []struct {
			u string
			w []byte
		}{{srcRedisURL, sw}, {dstRedisURL, tw}} {
			c, e := cache.ConfiguredRedis(pair.u, "", time.Second)
			if e != nil || c.SetEX(ctx, "ylreader:handover:"+partialEnv.plan.OperationID, pair.w, 2*time.Minute) != nil {
				t.Fatal("seed redis witness")
			}
		}
		secDir := filepath.Join(partialEnv.root, "secrets")
		writeSec := func(name string, b []byte) string {
			p := filepath.Join(secDir, name)
			if os.WriteFile(p, b, 0600) != nil {
				t.Fatal("write sec")
			}
			return p
		}
		sort.Strings(manifest.HeaderKIDs)
		rt := map[string]string{
			"target_instance_id":  partialEnv.cfg.Target.InstanceID,
			"config_revision_id":  "60000000-0000-4000-8000-000000000001",
			"jwt_revision_id":     "60000000-0000-4000-8000-000000000002",
			"keyring_revision_id": "60000000-0000-4000-8000-000000000003",
			"current_header_kid":  "primary",
		}
		spec := map[string]any{
			"handover_format":     "ylreader-handover-execution-spec",
			"handover_version":    1,
			"mode":                "partial_review",
			"source_database":     map[string]any{"system_identifier": srcPin.sys, "database_oid": srcPin.dbOID, "role_oid": srcPin.rID},
			"target_database":     map[string]any{"system_identifier": dstPartialPin.sys, "database_oid": dstPartialPin.dbOID, "role_oid": dstPartialPin.rID},
			"expected_runtime":    rt,
			"actual_runtime":      rt,
			"source_jwt_file":     writeSec("src.jwt", bytes.Repeat([]byte("a"), 32)),
			"target_jwt_file":     writeSec("dst.jwt", bytes.Repeat([]byte("b"), 32)),
			"source_redis_url":    srcRedisURL,
			"target_redis_url":    dstRedisURL,
			"source_redis":        map[string]any{"run_id": srcRedisID, "database": 0},
			"target_redis":        map[string]any{"run_id": dstRedisID, "database": 0},
			"source_witness_file": writeSec("src.wit", sw),
			"target_witness_file": writeSec("dst.wit", tw),
		}
		specBytes, _ := json.Marshal(spec)
		if os.WriteFile(filepath.Join(partialEnv.inputDir, "handover.json"), specBytes, 0600) != nil {
			t.Fatal("write handover.json")
		}

		out, errout, code := runCLI("restore", "--input-root", partialEnv.inputDir, "--request-key", "request.json", "--config-key", "config.json", "--plan-key", "plan.json", "--plan-digest", partialEnv.plan.Digest, "--confirm-target", partialEnv.cfg.Target.InstanceID, "--owner", ownerID, "--header-keys", partialEnv.hdrPath, "--pg-restore", pgRestore, "--handover-key", "handover.json", "--timeout", "2m")
		var res backup.ControlledRestoreResult
		if code != 4 || json.Unmarshal(out, &res) != nil || res.StatusCode != "RESTORE_HANDOVER_PARTIAL_UNREGISTERED" || res.Phase != "validating" || res.DatabaseOutcome != "committed" || !res.HeadersChecked || res.PublicationState != "verified" || res.HandoverReviewStatus != "partial_saved" || res.RegistrationState != "absent" || !res.ArtifactsRetained || res.RetrySafe || res.ExecutionAuthorized || res.ReadyForCutover || res.DeploymentAuthorized {
			t.Fatalf("partial handover restore unexpected output: code=%d out=%s err=%s", code, out, errout)
		}
		assertNoSecrets(partialEnv, string(out)+string(errout))
		assertScratchDiskState(t, partialEnv, true, "committed")

		stOut, _, stCode := runCLI("status", "--control-root", partialEnv.controlDir, "--operation", partialEnv.plan.OperationID, "--format", "json")
		var st backup.RestoreStatus
		if stCode != 4 || json.Unmarshal(stOut, &st) != nil || st.Status != "blocked" || st.Reason != "report_checks_incomplete" || st.ReportClaims != "partial" || st.Phase != "validating" || st.Registration != "absent" || st.Ready || st.Deployment {
			t.Fatalf("unexpected status after partial handover restore: code=%d out=%s", stCode, stOut)
		}
	})

	// Reuse rolled-back empty target DB `yreader_a1_iso_restore_fail` with pre-existing invalid publication receipt
	// to trigger real ErrPublication inside PublishObjects and verify .scratch-* remains intact on disk.
	pubFailEnv := newEnv("fail")
	opDir := filepath.Join(pubFailEnv.controlDir, "operations", pubFailEnv.plan.OperationID)
	if err := os.MkdirAll(opDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opDir, "publication.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	pubOut, pubErrOut, pubCode := runCLI("restore", "--input-root", pubFailEnv.inputDir, "--request-key", "request.json", "--config-key", "config.json", "--plan-key", "plan.json", "--plan-digest", pubFailEnv.plan.Digest, "--confirm-target", pubFailEnv.cfg.Target.InstanceID, "--owner", ownerID, "--header-keys", pubFailEnv.hdrPath, "--pg-restore", pgRestore, "--timeout", "2m")
	var pubRes backup.ControlledRestoreResult
	if pubCode != 4 || json.Unmarshal(pubOut, &pubRes) != nil || pubRes.StatusCode != "RESTORE_PUBLICATION_FAILED" || pubRes.Outcome != "publication_failed" || pubRes.DatabaseOutcome != "committed" || !pubRes.ArtifactsRetained {
		t.Fatalf("publication failure did not retain expected failure state: code=%d out=%s err=%s", pubCode, pubOut, pubErrOut)
	}
	assertNoSecrets(pubFailEnv, string(pubOut)+string(pubErrOut))
	assertScratchDiskState(t, pubFailEnv, true, "committed")

	t.Log(`BACKUP_RESTORE_CLI_EVIDENCE {"scope":"isolated-controlled-restore-cli","checks":7,"phase":"validating","partial_registration_denied":true,"replay_denied":true,"execution_authorized":false,"ready_for_cutover":false,"deployment_authorized":false}`)
}
