package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

type reviewSwapFilesystem struct {
	delegate *commitTestFilesystem
	orphan   string
	retained string
	target   string
	armed    bool
	swapped  bool
}

func (filesystem *reviewSwapFilesystem) Sync(file *os.File) error {
	return filesystem.delegate.Sync(file)
}

func (filesystem *reviewSwapFilesystem) RenameNoReplace(
	oldDirectory *os.File,
	oldName string,
	newDirectory *os.File,
	newName string,
) error {
	if filesystem.armed && !filesystem.swapped &&
		filepath.Base(oldDirectory.Name()) == filesystem.target &&
		oldName == filesystem.orphan {
		if err := unix.Renameat2(
			int(oldDirectory.Fd()), filesystem.orphan,
			int(oldDirectory.Fd()), filesystem.retained,
			unix.RENAME_EXCHANGE,
		); err != nil {
			return err
		}
		filesystem.swapped = true
	}
	return filesystem.delegate.RenameNoReplace(
		oldDirectory, oldName, newDirectory, newName,
	)
}

func (filesystem *reviewSwapFilesystem) RenameReplace(
	oldDirectory *os.File,
	oldName string,
	newDirectory *os.File,
	newName string,
) error {
	return filesystem.delegate.RenameReplace(
		oldDirectory, oldName, newDirectory, newName,
	)
}

func (filesystem *reviewSwapFilesystem) RemoveAt(
	directory *os.File,
	name string,
	flags int,
) error {
	return filesystem.delegate.RemoveAt(directory, name, flags)
}

func TestReviewDiskGCRevalidatesNameAtObjectUnlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := &reviewSwapFilesystem{
		delegate: newCommitTestFilesystem(),
		target:   "objects",
	}
	store := newDiskGenerationStoreWithDependencies(
		root,
		diskGenerationStoreDependencies{
			GenerationRandom: commitTestRandom(
				0x11, 0x12, 0x13, 0x14,
			),
			FS:        filesystem,
			FaultHook: (&commitTestFaultHook{}).invoke,
		},
	)
	objects := [][]byte{
		commitTestObject("A"),
		commitTestObject("B"),
		commitTestObject("C"),
		commitTestObject("D"),
	}
	parent := ""
	for _, object := range objects[:3] {
		result, err, _ := commitTestCommit(
			t, store, commitTestCandidate(parent, object),
		)
		parent = commitTestRequireCommitted(
			t, result, err,
		).Generation.GenerationID
	}
	filesystem.orphan = commitTestObjectDigest(objects[0]) + ".json"
	filesystem.retained = commitTestObjectDigest(objects[3]) + ".json"
	filesystem.armed = true
	result, err, _ := commitTestCommit(
		t, store, commitTestCandidate(parent, objects[3]),
	)
	commitTestRequireCommitted(t, result, err)
	if !filesystem.swapped {
		t.Fatal("review swap did not run")
	}
	if _, err := store.LoadCurrent(context.Background()); err != nil {
		t.Fatalf("GC name race corrupted committed current: %v", err)
	}
}

func TestReviewDiskGCRevalidatesNameAtGenerationRmdir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := &reviewSwapFilesystem{
		delegate: newCommitTestFilesystem(),
		orphan:   commitTestID(0x11),
		retained: commitTestID(0x14),
		target:   "generations",
	}
	store := newDiskGenerationStoreWithDependencies(
		root,
		diskGenerationStoreDependencies{
			GenerationRandom: commitTestRandom(
				0x11, 0x12, 0x13, 0x14,
			),
			FS:        filesystem,
			FaultHook: (&commitTestFaultHook{}).invoke,
		},
	)
	objects := [][]byte{
		commitTestObject("A"),
		commitTestObject("B"),
		commitTestObject("C"),
		commitTestObject("D"),
	}
	parent := ""
	for _, object := range objects[:3] {
		result, err, _ := commitTestCommit(
			t, store, commitTestCandidate(parent, object),
		)
		parent = commitTestRequireCommitted(
			t, result, err,
		).Generation.GenerationID
	}
	filesystem.armed = true
	result, err, _ := commitTestCommit(
		t, store, commitTestCandidate(parent, objects[3]),
	)
	commitTestRequireCommitted(t, result, err)
	if !filesystem.swapped {
		t.Fatal("review swap did not run")
	}
	if _, err := store.LoadCurrent(context.Background()); err != nil {
		t.Fatalf("GC generation name race corrupted committed current: %v", err)
	}
}
