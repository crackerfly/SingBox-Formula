package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

var errCommitTestInjected = errors.New("injected disk commit failure")

type commitTestFSOperation struct {
	Kind         string
	OldDirectory string
	OldName      string
	NewDirectory string
	NewName      string
	Path         string
	RemoveFlags  int
	IsDirectory  bool
	StatError    error
}

type commitTestFilesystem struct {
	mutex sync.Mutex

	operations []commitTestFSOperation
	failAt     int

	blockCurrentRename bool
	currentRenameReady chan struct{}
	currentRenameGo    chan struct{}
	currentRenameOnce  sync.Once

	failRootSyncAfterCurrent string
	currentRenameSucceeded   bool

	failRemovePath       string
	failRemoveNamePrefix string
}

func newCommitTestFilesystem() *commitTestFilesystem {
	return &commitTestFilesystem{}
}

func (filesystem *commitTestFilesystem) Sync(file *os.File) error {
	info, statErr := file.Stat()
	operation := commitTestFSOperation{
		Kind: "sync", Path: file.Name(),
	}
	if statErr == nil {
		operation.IsDirectory = info.IsDir()
	} else {
		operation.StatError = statErr
	}
	if filesystem.record(operation) {
		return errCommitTestInjected
	}
	filesystem.mutex.Lock()
	failPostCurrentRootSync :=
		filesystem.currentRenameSucceeded &&
			filesystem.failRootSyncAfterCurrent != "" &&
			filepath.Clean(file.Name()) ==
				filepath.Clean(filesystem.failRootSyncAfterCurrent)
	filesystem.mutex.Unlock()
	if failPostCurrentRootSync {
		return errCommitTestInjected
	}
	return file.Sync()
}

func (filesystem *commitTestFilesystem) RenameNoReplace(
	oldDirectory *os.File,
	oldName string,
	newDirectory *os.File,
	newName string,
) error {
	operation := commitTestFSOperation{
		Kind:         "rename_no_replace",
		OldDirectory: oldDirectory.Name(),
		OldName:      oldName,
		NewDirectory: newDirectory.Name(),
		NewName:      newName,
	}
	if filesystem.record(operation) {
		return errCommitTestInjected
	}
	return unix.Renameat2(
		int(oldDirectory.Fd()), oldName,
		int(newDirectory.Fd()), newName,
		unix.RENAME_NOREPLACE,
	)
}

func (filesystem *commitTestFilesystem) RenameReplace(
	oldDirectory *os.File,
	oldName string,
	newDirectory *os.File,
	newName string,
) error {
	operation := commitTestFSOperation{
		Kind:         "rename_replace",
		OldDirectory: oldDirectory.Name(),
		OldName:      oldName,
		NewDirectory: newDirectory.Name(),
		NewName:      newName,
	}
	fail := filesystem.record(operation)
	if newName == "current" {
		filesystem.mutex.Lock()
		block := filesystem.blockCurrentRename
		ready := filesystem.currentRenameReady
		release := filesystem.currentRenameGo
		filesystem.mutex.Unlock()
		if block {
			filesystem.currentRenameOnce.Do(func() {
				close(ready)
			})
			<-release
		}
	}
	if fail {
		return errCommitTestInjected
	}
	err := unix.Renameat(
		int(oldDirectory.Fd()), oldName,
		int(newDirectory.Fd()), newName,
	)
	if err == nil && newName == "current" {
		filesystem.mutex.Lock()
		filesystem.currentRenameSucceeded = true
		filesystem.mutex.Unlock()
	}
	return err
}

func (filesystem *commitTestFilesystem) RemoveAt(
	directory *os.File,
	name string,
	flags int,
) error {
	path := filepath.Join(directory.Name(), name)
	if filesystem.record(commitTestFSOperation{
		Kind:         "remove_at",
		OldDirectory: directory.Name(),
		OldName:      name,
		Path:         path,
		RemoveFlags:  flags,
	}) {
		return errCommitTestInjected
	}
	if flags != 0 && flags != unix.AT_REMOVEDIR {
		return unix.EINVAL
	}
	filesystem.mutex.Lock()
	fail := filesystem.failRemovePath != "" &&
		filepath.Clean(filesystem.failRemovePath) == filepath.Clean(path)
	if !fail && filesystem.failRemoveNamePrefix != "" {
		fail = strings.HasPrefix(
			filepath.Base(path),
			filesystem.failRemoveNamePrefix,
		)
	}
	filesystem.mutex.Unlock()
	if fail {
		return errCommitTestInjected
	}
	return unix.Unlinkat(int(directory.Fd()), name, flags)
}

func (filesystem *commitTestFilesystem) record(
	operation commitTestFSOperation,
) bool {
	filesystem.mutex.Lock()
	defer filesystem.mutex.Unlock()
	filesystem.operations = append(filesystem.operations, operation)
	return filesystem.failAt > 0 &&
		len(filesystem.operations) == filesystem.failAt
}

func (filesystem *commitTestFilesystem) reset() {
	filesystem.mutex.Lock()
	defer filesystem.mutex.Unlock()
	filesystem.operations = nil
	filesystem.failAt = 0
	filesystem.blockCurrentRename = false
	filesystem.currentRenameReady = nil
	filesystem.currentRenameGo = nil
	filesystem.currentRenameOnce = sync.Once{}
	filesystem.failRootSyncAfterCurrent = ""
	filesystem.currentRenameSucceeded = false
	filesystem.failRemovePath = ""
	filesystem.failRemoveNamePrefix = ""
}

func (filesystem *commitTestFilesystem) snapshot() []commitTestFSOperation {
	filesystem.mutex.Lock()
	defer filesystem.mutex.Unlock()
	return append(
		[]commitTestFSOperation(nil), filesystem.operations...,
	)
}

func (filesystem *commitTestFilesystem) failOperation(
	index int,
) {
	filesystem.mutex.Lock()
	defer filesystem.mutex.Unlock()
	filesystem.failAt = index + 1
}

func (filesystem *commitTestFilesystem) blockCurrent(
	ready chan struct{},
	release chan struct{},
) {
	filesystem.mutex.Lock()
	defer filesystem.mutex.Unlock()
	filesystem.blockCurrentRename = true
	filesystem.currentRenameReady = ready
	filesystem.currentRenameGo = release
}

func (filesystem *commitTestFilesystem) failPostCurrentRootSync(
	root string,
) {
	filesystem.mutex.Lock()
	defer filesystem.mutex.Unlock()
	filesystem.failRootSyncAfterCurrent = root
}

func (filesystem *commitTestFilesystem) failRemove(path string) {
	filesystem.mutex.Lock()
	defer filesystem.mutex.Unlock()
	filesystem.failRemovePath = path
}

func (filesystem *commitTestFilesystem) failRemovePrefix(
	prefix string,
) {
	filesystem.mutex.Lock()
	defer filesystem.mutex.Unlock()
	filesystem.failRemoveNamePrefix = prefix
}

type commitTestFaultHook struct {
	mutex sync.Mutex

	stages    []string
	failStage string

	blockStage string
	blockReady chan struct{}
	blockGo    chan struct{}
	blockOnce  sync.Once
}

func (hook *commitTestFaultHook) invoke(stage string) error {
	hook.mutex.Lock()
	hook.stages = append(hook.stages, stage)
	fail := stage == hook.failStage
	block := stage == hook.blockStage
	ready := hook.blockReady
	release := hook.blockGo
	hook.mutex.Unlock()
	if block {
		hook.blockOnce.Do(func() {
			close(ready)
		})
		<-release
	}
	if fail {
		return errCommitTestInjected
	}
	return nil
}

func (hook *commitTestFaultHook) reset() {
	hook.mutex.Lock()
	defer hook.mutex.Unlock()
	hook.stages = nil
	hook.failStage = ""
	hook.blockStage = ""
	hook.blockReady = nil
	hook.blockGo = nil
	hook.blockOnce = sync.Once{}
}

func (hook *commitTestFaultHook) fail(stage string) {
	hook.mutex.Lock()
	defer hook.mutex.Unlock()
	hook.failStage = stage
}

func (hook *commitTestFaultHook) block(
	stage string,
	ready chan struct{},
	release chan struct{},
) {
	hook.mutex.Lock()
	defer hook.mutex.Unlock()
	hook.blockStage = stage
	hook.blockReady = ready
	hook.blockGo = release
}

func (hook *commitTestFaultHook) snapshot() []string {
	hook.mutex.Lock()
	defer hook.mutex.Unlock()
	return append([]string(nil), hook.stages...)
}

func commitTestRandom(values ...byte) io.Reader {
	var contents []byte
	for _, value := range values {
		contents = append(contents, bytes.Repeat([]byte{value}, 32)...)
	}
	return bytes.NewReader(contents)
}

func commitTestID(value byte) string {
	return strings.Repeat(hex.EncodeToString([]byte{value}), 32)
}

func commitTestObject(tag string) []byte {
	return []byte(
		`{"outbounds":[{"tag":"` + tag + `","type":"direct"}]}`,
	)
}

func commitTestObjectDigest(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func commitTestCandidate(
	parent string,
	object []byte,
) generationCandidate {
	urlDigest := strings.Repeat("d", 64)
	return generationCandidate{
		ParentGenerationID: parent,
		ConfigDigest:       strings.Repeat("c", 64),
		State:              generationStateFresh,
		Objects:            [][]byte{append([]byte(nil), object...)},
		Sources: []generationCandidateSource{{
			Index:       1,
			ObjectIndex: 1,
			URLDigest:   urlDigest,
			Result:      sourceResultFresh,
			FetchCode:   fetchCodeOK,
			Info: NormalizeInfo{
				Format:   FormatSingBoxJSON,
				Accepted: 1,
				Skipped:  0,
				Warnings: []Warning{},
			},
		}},
	}
}

func newCommitTestStore(
	root string,
	random io.Reader,
	filesystem *commitTestFilesystem,
	hook *commitTestFaultHook,
) generationStore {
	return newDiskGenerationStoreWithDependencies(
		root,
		diskGenerationStoreDependencies{
			GenerationRandom: random,
			FS:               filesystem,
			FaultHook:        hook.invoke,
		},
	)
}

func commitTestCommit(
	t *testing.T,
	store generationStore,
	candidate generationCandidate,
) (generationCommitResult, error, *currentCommitGate) {
	t.Helper()
	ctx, gate := withNewCurrentCommitGate(context.Background())
	result, err := store.Commit(ctx, candidate)
	return result, err, gate
}

func commitTestRequireCommitted(
	t *testing.T,
	result generationCommitResult,
	err error,
) currentSelection {
	t.Helper()
	if !result.Committed {
		t.Fatalf("commit failed: result=%#v err=%v", result, err)
	}
	if result.Selection.Kind != currentPresent {
		t.Fatalf("committed selection = %#v", result.Selection)
	}
	return result.Selection
}

func commitTestReadCurrent(t *testing.T, root string) []byte {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, "current"))
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	return contents
}

func commitTestRequireOldCurrent(
	t *testing.T,
	root string,
	store generationStore,
	oldCurrent []byte,
	oldGeneration string,
) {
	t.Helper()
	if got := commitTestReadCurrent(t, root); !bytes.Equal(
		got, oldCurrent,
	) {
		t.Fatalf("current changed: got %q want %q", got, oldCurrent)
	}
	selection, err := store.LoadCurrent(context.Background())
	if err != nil {
		t.Fatalf("load preserved current: %v", err)
	}
	if selection.Kind != currentPresent ||
		selection.Generation.GenerationID != oldGeneration {
		t.Fatalf("preserved selection = %#v", selection)
	}
}

func commitTestPrepareRoot(
	t *testing.T,
	root string,
) {
	t.Helper()
	for _, directory := range []string{
		root,
		filepath.Join(root, "generations"),
		filepath.Join(root, "objects"),
	} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatalf("mkdir %s: %v", directory, err)
		}
		if err := os.Chmod(directory, 0700); err != nil {
			t.Fatalf("chmod %s: %v", directory, err)
		}
	}
}

func commitTestAwait[T any](
	t *testing.T,
	channel <-chan T,
	label string,
) T {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case value := <-channel:
		return value
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", label)
		var zero T
		return zero
	}
}
