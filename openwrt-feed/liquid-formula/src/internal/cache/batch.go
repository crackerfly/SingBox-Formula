// Package cache provides validated, rollback-capable cache transactions.
package cache

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

var (
	ErrDuplicateDestination = errors.New("duplicate cache destination")
	ErrBatchClosed          = errors.New("cache batch is closed")
)

type Validator func(stagedPath string) error

type fileOps struct {
	rename func(string, string) error
	remove func(string) error
}

func defaultFileOps() fileOps {
	return fileOps{rename: os.Rename, remove: os.Remove}
}

type entry struct {
	destination string
	stage       string
	backup      string
	existed     bool
	replaced    bool
}

type Batch struct {
	entries      []*entry
	destinations map[string]struct{}
	ops          fileOps
	closed       bool
}

func NewBatch() *Batch {
	return newBatchWithOps(defaultFileOps())
}

func newBatchWithOps(ops fileOps) *Batch {
	if ops.rename == nil {
		ops.rename = os.Rename
	}
	if ops.remove == nil {
		ops.remove = os.Remove
	}
	return &Batch{destinations: make(map[string]struct{}), ops: ops}
}

// Stage writes and syncs a candidate beside destination, then validates the
// closed staging file. Formal destinations are never opened or removed here.
func (b *Batch) Stage(destination string, data []byte, mode fs.FileMode, validate Validator) (result error) {
	if b == nil || b.closed {
		return ErrBatchClosed
	}
	absDestination, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve cache destination: %w", err)
	}
	absDestination = filepath.Clean(absDestination)
	if _, exists := b.destinations[absDestination]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateDestination, absDestination)
	}
	if err := os.MkdirAll(filepath.Dir(absDestination), 0o755); err != nil {
		return fmt.Errorf("create cache destination directory: %w", err)
	}

	stage, err := os.CreateTemp(filepath.Dir(absDestination), "."+filepath.Base(absDestination)+".stage-*")
	if err != nil {
		return fmt.Errorf("create cache stage: %w", err)
	}
	stagePath := stage.Name()
	defer func() {
		if result != nil {
			_ = stage.Close()
			_ = b.ops.remove(stagePath)
		}
	}()
	if err := stage.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("set cache stage mode: %w", err)
	}
	if _, err := stage.Write(data); err != nil {
		return fmt.Errorf("write cache stage: %w", err)
	}
	if err := stage.Sync(); err != nil {
		return fmt.Errorf("sync cache stage: %w", err)
	}
	if err := stage.Close(); err != nil {
		return fmt.Errorf("close cache stage: %w", err)
	}
	if validate != nil {
		if err := validate(stagePath); err != nil {
			return fmt.Errorf("validate cache stage %s: %w", absDestination, err)
		}
	}

	b.entries = append(b.entries, &entry{destination: absDestination, stage: stagePath})
	b.destinations[absDestination] = struct{}{}
	return nil
}

// Commit replaces every destination only after every Stage call has already
// validated. Existing files are copied to synced same-directory backups; any
// replacement failure restores all earlier destinations before returning.
func (b *Batch) Commit() error {
	if b == nil || b.closed {
		return ErrBatchClosed
	}
	b.closed = true

	for _, item := range b.entries {
		info, err := os.Lstat(item.destination)
		switch {
		case err == nil:
			if !info.Mode().IsRegular() {
				return b.fail(fmt.Errorf("cache destination is not a regular file: %s", item.destination))
			}
			item.existed = true
			backup, err := createBackup(item.destination, info.Mode().Perm())
			if err != nil {
				return b.fail(fmt.Errorf("backup cache destination %s: %w", item.destination, err))
			}
			item.backup = backup
		case errors.Is(err, os.ErrNotExist):
			item.existed = false
		default:
			return b.fail(fmt.Errorf("inspect cache destination %s: %w", item.destination, err))
		}
	}

	for _, item := range b.entries {
		if err := b.ops.rename(item.stage, item.destination); err != nil {
			return b.fail(fmt.Errorf("replace cache destination %s: %w", item.destination, err))
		}
		item.stage = ""
		item.replaced = true
	}
	if err := syncDirectories(b.entries); err != nil {
		return b.fail(fmt.Errorf("sync committed cache directories: %w", err))
	}

	// Once every replacement and directory sync has succeeded, the transaction
	// is committed. Cleanup is best-effort and must not turn a committed disk
	// generation into an error that would prevent the matching memory snapshot
	// from being applied.
	for _, item := range b.entries {
		if item.backup != "" {
			if err := removeIfExists(b.ops.remove, item.backup); err == nil {
				item.backup = ""
			}
		}
	}
	_ = syncDirectories(b.entries)
	return nil
}

// Abort removes only transaction-owned stages and backups. It is idempotent
// and never removes or truncates a formal destination.
func (b *Batch) Abort() error {
	if b == nil {
		return nil
	}
	if b.closed {
		return b.cleanupTransactionFiles()
	}
	b.closed = true
	return b.cleanupTransactionFiles()
}

func (b *Batch) fail(cause error) error {
	rollbackErr := b.rollback()
	cleanupErr := b.cleanupTransactionFiles()
	return errors.Join(cause, rollbackErr, cleanupErr)
}

func (b *Batch) rollback() error {
	var result error
	for i := len(b.entries) - 1; i >= 0; i-- {
		item := b.entries[i]
		if !item.replaced {
			continue
		}
		if item.existed {
			if item.backup == "" {
				result = errors.Join(result, fmt.Errorf("missing rollback backup for %s", item.destination))
				continue
			}
			if err := b.ops.rename(item.backup, item.destination); err != nil {
				result = errors.Join(result, fmt.Errorf("restore %s from retained backup %s: %w", item.destination, item.backup, err))
				continue
			} else {
				item.backup = ""
			}
		} else if err := removeIfExists(b.ops.remove, item.destination); err != nil {
			result = errors.Join(result, fmt.Errorf("remove new destination %s: %w", item.destination, err))
			continue
		}
		item.replaced = false
	}
	if err := syncDirectories(b.entries); err != nil {
		result = errors.Join(result, fmt.Errorf("sync rolled back cache directories: %w", err))
	}
	return result
}

func (b *Batch) cleanupTransactionFiles() error {
	var result error
	for _, item := range b.entries {
		if item.stage != "" {
			if err := removeIfExists(b.ops.remove, item.stage); err != nil {
				result = errors.Join(result, err)
			} else {
				item.stage = ""
			}
		}
		// A failed restore leaves replaced=true. That backup is the only
		// last-known-good recovery copy and must survive both Commit failure
		// cleanup and a caller's deferred Abort.
		if item.backup != "" && !item.replaced {
			if err := removeIfExists(b.ops.remove, item.backup); err != nil {
				result = errors.Join(result, err)
			} else {
				item.backup = ""
			}
		}
	}
	return result
}

func createBackup(source string, mode fs.FileMode) (result string, resultErr error) {
	src, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer src.Close()

	backup, err := os.CreateTemp(filepath.Dir(source), "."+filepath.Base(source)+".backup-*")
	if err != nil {
		return "", err
	}
	backupPath := backup.Name()
	defer func() {
		if resultErr != nil {
			_ = backup.Close()
			_ = os.Remove(backupPath)
		}
	}()
	if err := backup.Chmod(mode.Perm()); err != nil {
		return "", err
	}
	if _, err := io.Copy(backup, src); err != nil {
		return "", err
	}
	if err := backup.Sync(); err != nil {
		return "", err
	}
	if err := backup.Close(); err != nil {
		return "", err
	}
	return backupPath, nil
}

func removeIfExists(remove func(string) error, path string) error {
	if err := remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func syncDirectories(entries []*entry) error {
	directories := make(map[string]struct{})
	for _, item := range entries {
		directories[filepath.Dir(item.destination)] = struct{}{}
	}
	ordered := make([]string, 0, len(directories))
	for directory := range directories {
		ordered = append(ordered, directory)
	}
	sort.Strings(ordered)
	for _, directory := range ordered {
		file, err := os.Open(directory)
		if err != nil {
			return err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
