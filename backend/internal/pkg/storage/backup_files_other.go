//go:build !linux

package storage

// Native ACL/handle-relative durability must be implemented and tested first.
func OpenBackupFiles(string) (BackupFiles, error) { return nil, ErrCapability }
