# Backup envelope design experiment

This module contains tests only. It does not implement the application's backup
CLI, use user data, or modify backend dependencies. Go 1.26.11 is required.

From this directory, with Go on PATH and writable GOPATH/GOCACHE/GOTMPDIR:

```powershell
go mod download
go mod verify
go build -mod=readonly -o ../../../.toolchain/backup-age.exe filippo.io/age/cmd/age
$env:BACKUP_AGE_CLI = (Resolve-Path ../../../.toolchain/backup-age.exe).Path
go test -count=1 ./...
go test -mod=readonly -count=1 filippo.io/age
```

The first command requires network access; module checksum verification must
remain enabled. The Go tool directive keeps official CLI dependencies in go.sum.
CLI and library interoperability uses the same upstream implementation, not two
independent crypto implementations. Upstream TestVectors additionally exercises
the pinned CCTV vectors. Neither test extracts untrusted files or touches a DB.

For schema checks, from the repository root:

```powershell
python -m pip install --target .toolchain/backup-schema-deps -r scripts/prototypes/backup-age/schema-requirements.txt
python scripts/verify-backup-contract.py
```

Schema checks never open referenced DSN/identity files. Paths in docs/examples
are synthetic. This is not a production authorization or physical path check.
