package cache

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBatchStagesBesideDestinationAndCommitsAll(t *testing.T) {
	dir := t.TempDir()
	node := filepath.Join(dir, "node.json")
	template := filepath.Join(dir, "template.json")
	writeTestFile(t, node, "old-node")
	writeTestFile(t, template, "old-template")

	batch := NewBatch()
	t.Cleanup(func() { _ = batch.Abort() })
	validator := func(path string) error {
		if filepath.Dir(path) != dir {
			t.Fatalf("stage directory = %q, want %q", filepath.Dir(path), dir)
		}
		if got := readTestFile(t, node); got != "old-node" {
			t.Fatalf("node changed during validation: %q", got)
		}
		if got := readTestFile(t, template); got != "old-template" {
			t.Fatalf("template changed during validation: %q", got)
		}
		return nil
	}
	if err := batch.Stage(node, []byte("new-node"), 0o600, validator); err != nil {
		t.Fatalf("Stage(node) error = %v", err)
	}
	if err := batch.Stage(template, []byte("new-template"), 0o600, validator); err != nil {
		t.Fatalf("Stage(template) error = %v", err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if got := readTestFile(t, node); got != "new-node" {
		t.Fatalf("node after commit = %q", got)
	}
	if got := readTestFile(t, template); got != "new-template" {
		t.Fatalf("template after commit = %q", got)
	}
	assertNoTransactionFiles(t, dir)
}

func TestBatchValidationFailurePreservesDestinations(t *testing.T) {
	dir := t.TempDir()
	node := filepath.Join(dir, "node.json")
	template := filepath.Join(dir, "template.json")
	writeTestFile(t, node, "old-node")
	writeTestFile(t, template, "old-template")
	validationErr := errors.New("invalid template")

	batch := NewBatch()
	if err := batch.Stage(node, []byte("new-node"), 0o600, nil); err != nil {
		t.Fatalf("Stage(node) error = %v", err)
	}
	if err := batch.Stage(template, []byte("new-template"), 0o600, func(string) error { return validationErr }); !errors.Is(err, validationErr) {
		t.Fatalf("Stage(template) error = %v, want %v", err, validationErr)
	}
	if err := batch.Abort(); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if got := readTestFile(t, node); got != "old-node" {
		t.Fatalf("node after validation failure = %q", got)
	}
	if got := readTestFile(t, template); got != "old-template" {
		t.Fatalf("template after validation failure = %q", got)
	}
	assertNoTransactionFiles(t, dir)
}

func TestBatchCommitFailureRollsBackAllDestinations(t *testing.T) {
	dir := t.TempDir()
	node := filepath.Join(dir, "node.json")
	template := filepath.Join(dir, "template.json")
	writeTestFile(t, node, "old-node")
	writeTestFile(t, template, "old-template")
	injected := errors.New("second replacement failed")
	stageRenames := 0
	ops := defaultFileOps()
	realRename := ops.rename
	ops.rename = func(oldPath, newPath string) error {
		if strings.Contains(filepath.Base(oldPath), ".stage-") {
			stageRenames++
			if stageRenames == 2 {
				return injected
			}
		}
		return realRename(oldPath, newPath)
	}

	batch := newBatchWithOps(ops)
	if err := batch.Stage(node, []byte("new-node"), 0o600, nil); err != nil {
		t.Fatalf("Stage(node) error = %v", err)
	}
	if err := batch.Stage(template, []byte("new-template"), 0o600, nil); err != nil {
		t.Fatalf("Stage(template) error = %v", err)
	}
	if err := batch.Commit(); !errors.Is(err, injected) {
		t.Fatalf("Commit() error = %v, want %v", err, injected)
	}
	if got := readTestFile(t, node); got != "old-node" {
		t.Fatalf("node after rollback = %q", got)
	}
	if got := readTestFile(t, template); got != "old-template" {
		t.Fatalf("template after rollback = %q", got)
	}
	assertNoTransactionFiles(t, dir)
}

func TestBatchRetainsBackupWhenRollbackRestoreFails(t *testing.T) {
	dir := t.TempDir()
	node := filepath.Join(dir, "node.json")
	template := filepath.Join(dir, "template.json")
	writeTestFile(t, node, "old-node")
	writeTestFile(t, template, "old-template")
	replaceErr := errors.New("second replacement failed")
	restoreErr := errors.New("rollback restore failed")
	stageRenames := 0
	ops := defaultFileOps()
	realRename := ops.rename
	ops.rename = func(oldPath, newPath string) error {
		base := filepath.Base(oldPath)
		if strings.Contains(base, ".stage-") {
			stageRenames++
			if stageRenames == 2 {
				return replaceErr
			}
		}
		if strings.Contains(base, ".backup-") && newPath == node {
			return restoreErr
		}
		return realRename(oldPath, newPath)
	}

	batch := newBatchWithOps(ops)
	if err := batch.Stage(node, []byte("new-node"), 0o600, nil); err != nil {
		t.Fatalf("Stage(node) error = %v", err)
	}
	if err := batch.Stage(template, []byte("new-template"), 0o600, nil); err != nil {
		t.Fatalf("Stage(template) error = %v", err)
	}
	err := batch.Commit()
	if !errors.Is(err, replaceErr) || !errors.Is(err, restoreErr) {
		t.Fatalf("Commit() error = %v, want replacement and restore errors", err)
	}
	if abortErr := batch.Abort(); abortErr != nil {
		t.Fatalf("Abort() after failed restore error = %v", abortErr)
	}

	backups, globErr := filepath.Glob(filepath.Join(dir, ".node.json.backup-*"))
	if globErr != nil {
		t.Fatalf("glob recovery backup: %v", globErr)
	}
	if len(backups) != 1 {
		t.Fatalf("recovery backups = %v, want exactly one retained backup", backups)
	}
	if got := readTestFile(t, backups[0]); got != "old-node" {
		t.Fatalf("retained recovery backup = %q, want old-node", got)
	}
	if got := readTestFile(t, template); got != "old-template" {
		t.Fatalf("unreplaced template = %q, want old-template", got)
	}
}

func TestBatchAbortRemovesOnlyStagingFiles(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "node.json")
	writeTestFile(t, destination, "last-known-good")
	batch := NewBatch()
	if err := batch.Stage(destination, []byte("candidate"), 0o600, nil); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if err := batch.Abort(); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if err := batch.Abort(); err != nil {
		t.Fatalf("second Abort() error = %v", err)
	}
	if got := readTestFile(t, destination); got != "last-known-good" {
		t.Fatalf("destination after abort = %q", got)
	}
	assertNoTransactionFiles(t, dir)
}

func TestBatchRejectsDuplicateDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "node.json")
	batch := NewBatch()
	if err := batch.Stage(destination, []byte("first"), 0o600, nil); err != nil {
		t.Fatalf("first Stage() error = %v", err)
	}
	if err := batch.Stage(destination, []byte("second"), 0o600, nil); !errors.Is(err, ErrDuplicateDestination) {
		t.Fatalf("duplicate Stage() error = %v, want ErrDuplicateDestination", err)
	}
	_ = batch.Abort()
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertNoTransactionFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read transaction directory: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".stage-") || strings.Contains(entry.Name(), ".backup-") {
			t.Errorf("transaction file left behind: %s", entry.Name())
		}
	}
}
