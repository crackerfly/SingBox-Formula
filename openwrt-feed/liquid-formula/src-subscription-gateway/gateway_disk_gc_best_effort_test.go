package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiskGCBeforeHookFailureRemainsCommittedAndRetriesLater(
	t *testing.T,
) {
	seed := newCommitTestGCSeed(t)
	oldGeneration := filepath.Join(
		seed.root, "generations", seed.generations[0],
	)
	oldObject := filepath.Join(
		seed.root, "objects",
		commitTestObjectDigest(seed.objects[0])+".json",
	)
	seed.filesystem.reset()
	seed.hook.reset()
	seed.hook.fail("before_gc")

	result := seed.commitFourthNormally(t)
	if !result.Committed ||
		result.Selection.Generation.GenerationID != commitTestID(0x34) {
		t.Fatalf("before-GC fault result = %#v", result)
	}
	if got := commitTestReadCurrent(t, seed.root); !bytes.Equal(
		got, []byte(commitTestID(0x34)+"\n"),
	) {
		t.Fatalf("before-GC fault current = %q", got)
	}
	for _, operation := range seed.filesystem.snapshot() {
		if operation.Kind == "remove_at" &&
			(filepath.Clean(operation.Path) == filepath.Clean(oldObject) ||
				filepath.Clean(operation.Path) ==
					filepath.Clean(oldGeneration) ||
				commitTestPathWithin(operation.Path, oldGeneration)) {
			t.Fatalf(
				"orphan GC deletion ran after before_gc failed: %#v",
				operation,
			)
		}
	}
	if _, err := os.Lstat(oldGeneration); err != nil {
		t.Fatalf("before-GC fault removed generation: %v", err)
	}
	if _, err := os.Lstat(oldObject); err != nil {
		t.Fatalf("before-GC fault removed object: %v", err)
	}

	seed.filesystem.reset()
	seed.hook.reset()
	fifthObject := commitTestObject("E")
	fifthResult, fifthErr, _ := commitTestCommit(
		t,
		seed.store,
		commitTestCandidate(commitTestID(0x34), fifthObject),
	)
	fifth := commitTestRequireCommitted(t, fifthResult, fifthErr)
	if fifth.Generation.GenerationID != commitTestID(0x35) {
		t.Fatalf("retry generation = %q", fifth.Generation.GenerationID)
	}
	commitTestRequireAbsent(t, oldGeneration)
	commitTestRequireAbsent(t, oldObject)
	commitTestRequireEntryNames(
		t,
		filepath.Join(seed.root, "generations"),
		[]string{
			commitTestID(0x33),
			commitTestID(0x34),
			commitTestID(0x35),
		},
	)
	commitTestRequireEntryNames(
		t,
		filepath.Join(seed.root, "objects"),
		[]string{
			commitTestObjectDigest(seed.objects[2]) + ".json",
			commitTestObjectDigest(seed.objects[3]) + ".json",
			commitTestObjectDigest(fifthObject) + ".json",
		},
	)
}

func TestDiskGCEveryDeletionFailureRemainsCommittedAndAvoidsRetainedData(
	t *testing.T,
) {
	probe := newCommitTestGCSeed(t)
	probe.filesystem.reset()
	probe.hook.reset()
	probe.commitFourthNormally(t)
	type removal struct {
		relative string
		flags    int
	}
	var removals []removal
	oldGenerationQuarantinePrefix := filepath.Join(
		"generations",
		diskGCGenerationQuarantinePrefix+commitTestID(0x31)+"-",
	)
	oldObjectQuarantinePrefix := filepath.Join(
		"objects",
		diskGCObjectQuarantinePrefix+
			commitTestObjectDigest(probe.objects[0])+"-",
	)
	for _, operation := range probe.filesystem.snapshot() {
		if operation.Kind != "remove_at" {
			continue
		}
		if operation.RemoveFlags != 0 &&
			operation.RemoveFlags != unix.AT_REMOVEDIR {
			t.Fatalf("unexpected remove flags: %#v", operation)
		}
		relative, err := filepath.Rel(probe.root, operation.Path)
		if err != nil || relative == "." ||
			strings.HasPrefix(relative, "..") {
			t.Fatalf(
				"invalid removal path %q relative=%q err=%v",
				operation.Path, relative, err,
			)
		}
		if strings.HasPrefix(
			relative, oldObjectQuarantinePrefix,
		) || strings.HasPrefix(
			relative, oldGenerationQuarantinePrefix,
		) {
			removals = append(removals, removal{
				relative: relative,
				flags:    operation.RemoveFlags,
			})
		}
	}
	if len(removals) != 5 {
		t.Fatalf(
			"healthy GC removal trace = %#v, want generation files, directory and object",
			removals,
		)
	}
	generationFiles := map[string]bool{
		"aggregate.json": false,
		"manifest.json":  false,
		"status.json":    false,
	}
	generationDirectoryIndex := -1
	objectSeen := false
	for index, operation := range removals {
		switch {
		case strings.HasPrefix(
			operation.relative,
			oldObjectQuarantinePrefix,
		):
			if operation.flags != 0 ||
				filepath.Dir(operation.relative) != "objects" {
				t.Fatalf("unexpected object unlink = %#v", operation)
			}
			objectSeen = true
		case strings.HasPrefix(
			operation.relative,
			oldGenerationQuarantinePrefix,
		) && filepath.Dir(operation.relative) == "generations":
			if operation.flags != unix.AT_REMOVEDIR {
				t.Fatalf("generation rmdir flags = %#v", operation)
			}
			generationDirectoryIndex = index
		case strings.HasPrefix(
			filepath.Dir(operation.relative),
			oldGenerationQuarantinePrefix,
		):
			name := filepath.Base(operation.relative)
			if _, exists := generationFiles[name]; !exists ||
				operation.flags != 0 {
				t.Fatalf("unexpected generation unlink = %#v", operation)
			}
			generationFiles[name] = true
			if generationDirectoryIndex >= 0 {
				t.Fatalf(
					"generation file removed after rmdir: %#v", removals,
				)
			}
		default:
			t.Fatalf("unexpected GC removal = %#v", operation)
		}
	}
	if generationDirectoryIndex < 3 || !objectSeen {
		t.Fatalf("incomplete GC removal order = %#v", removals)
	}
	for name, seen := range generationFiles {
		if !seen {
			t.Fatalf("generation file %s was not unlinked: %#v", name, removals)
		}
	}

	for _, operation := range removals {
		t.Run(operation.relative, func(t *testing.T) {
			seed := newCommitTestGCSeed(t)
			seed.filesystem.reset()
			seed.hook.reset()
			seed.filesystem.failRemove(
				filepath.Join(seed.root, operation.relative),
			)

			result := seed.commitFourthNormally(t)
			if !result.Committed ||
				result.Selection.Generation.GenerationID !=
					commitTestID(0x34) {
				t.Fatalf(
					"delete failure %q result = %#v",
					operation.relative,
					result,
				)
			}
			if got := commitTestReadCurrent(t, seed.root); !bytes.Equal(
				got, []byte(commitTestID(0x34)+"\n"),
			) {
				t.Fatalf(
					"delete failure %q current = %q",
					operation.relative,
					got,
				)
			}
			commitTestRequireRetainedGCArtifacts(t, seed)
		})
	}
}

func TestDiskGCFailedObjectDeletionIsCollectedByNextSuccess(
	t *testing.T,
) {
	seed := newCommitTestGCSeed(t)
	oldObject := filepath.Join(
		seed.root, "objects",
		commitTestObjectDigest(seed.objects[0])+".json",
	)
	seed.filesystem.reset()
	seed.hook.reset()
	seed.filesystem.failRemovePrefix(
		diskGCObjectQuarantinePrefix +
			commitTestObjectDigest(seed.objects[0]) + "-",
	)
	seed.commitFourthNormally(t)
	if _, err := os.Lstat(oldObject); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("public orphan was not quarantined: %v", err)
	}
	quarantines, err := filepath.Glob(filepath.Join(
		seed.root,
		"objects",
		diskGCObjectQuarantinePrefix+
			commitTestObjectDigest(seed.objects[0])+"-*",
	))
	if err != nil || len(quarantines) != 1 {
		t.Fatalf(
			"injected object quarantine = %#v err=%v",
			quarantines,
			err,
		)
	}

	seed.filesystem.reset()
	seed.hook.reset()
	fifthObject := commitTestObject("E")
	fifthResult, fifthErr, _ := commitTestCommit(
		t,
		seed.store,
		commitTestCandidate(commitTestID(0x34), fifthObject),
	)
	commitTestRequireCommitted(t, fifthResult, fifthErr)
	commitTestRequireAbsent(t, oldObject)
	if _, err := os.Lstat(quarantines[0]); !errors.Is(
		err, os.ErrNotExist,
	) {
		t.Fatalf("object quarantine survived retry: %v", err)
	}
}

func TestDiskGCPartialGenerationQuarantineIsCollectedByNextSuccess(
	t *testing.T,
) {
	seed := newCommitTestGCSeed(t)
	oldGeneration := filepath.Join(
		seed.root, "generations", seed.generations[0],
	)
	seed.filesystem.reset()
	seed.hook.reset()
	seed.filesystem.failRemovePrefix("manifest.json")
	seed.commitFourthNormally(t)
	if _, err := os.Lstat(oldGeneration); !errors.Is(
		err, os.ErrNotExist,
	) {
		t.Fatalf("public stale generation was not quarantined: %v", err)
	}
	quarantines, err := filepath.Glob(filepath.Join(
		seed.root,
		"generations",
		diskGCGenerationQuarantinePrefix+
			seed.generations[0]+"-*",
	))
	if err != nil || len(quarantines) != 1 {
		t.Fatalf(
			"partial generation quarantine = %#v err=%v",
			quarantines,
			err,
		)
	}
	entries, err := os.ReadDir(quarantines[0])
	if err != nil || len(entries) != 2 {
		t.Fatalf(
			"partial generation entries = %#v err=%v",
			entries,
			err,
		)
	}

	seed.filesystem.reset()
	seed.hook.reset()
	fifthObject := commitTestObject("E")
	fifthResult, fifthErr, _ := commitTestCommit(
		t,
		seed.store,
		commitTestCandidate(commitTestID(0x34), fifthObject),
	)
	commitTestRequireCommitted(t, fifthResult, fifthErr)
	if _, err := os.Lstat(quarantines[0]); !errors.Is(
		err, os.ErrNotExist,
	) {
		t.Fatalf("partial generation quarantine survived retry: %v", err)
	}
	commitTestRequireAbsent(t, oldGeneration)
	if _, err := seed.store.LoadCurrent(
		context.Background(),
	); err != nil {
		t.Fatalf("recovered current is invalid: %v", err)
	}
}

func commitTestPathWithin(path string, directory string) bool {
	relative, err := filepath.Rel(directory, path)
	return err == nil &&
		relative != "." &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func commitTestRequireRetainedGCArtifacts(
	t *testing.T,
	seed commitTestGCSeed,
) {
	t.Helper()
	for _, generation := range []string{
		commitTestID(0x32),
		commitTestID(0x33),
		commitTestID(0x34),
	} {
		path := filepath.Join(seed.root, "generations", generation)
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("retained generation %s changed: %v", generation, err)
		}
	}
	for _, object := range seed.objects[1:] {
		path := filepath.Join(
			seed.root, "objects",
			commitTestObjectDigest(object)+".json",
		)
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf(
				"retained object %s changed: %v",
				commitTestObjectDigest(object), err,
			)
		}
	}
}
