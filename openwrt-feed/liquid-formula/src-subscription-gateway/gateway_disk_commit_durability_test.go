package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func commitTestTwoObjectCandidate() generationCandidate {
	first := commitTestObject("Alpha")
	second := commitTestObject("Beta")
	return generationCandidate{
		ConfigDigest: strings.Repeat("c", 64),
		State:        generationStateFresh,
		Objects: [][]byte{
			first,
			second,
		},
		Sources: []generationCandidateSource{
			{
				Index:       1,
				ObjectIndex: 1,
				URLDigest:   strings.Repeat("d", 64),
				Result:      sourceResultFresh,
				FetchCode:   fetchCodeOK,
				Info: NormalizeInfo{
					Format:   FormatSingBoxJSON,
					Accepted: 1,
					Skipped:  0,
					Warnings: []Warning{},
				},
			},
			{
				Index:       2,
				ObjectIndex: 2,
				URLDigest:   strings.Repeat("e", 64),
				Result:      sourceResultFresh,
				FetchCode:   fetchCodeOK,
				Info: NormalizeInfo{
					Format:   FormatSingBoxJSON,
					Accepted: 1,
					Skipped:  0,
					Warnings: []Warning{},
				},
			},
		},
	}
}

func TestDiskCommitCreatesOnlyContractModesAndRenameSemantics(
	t *testing.T,
) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	candidate := commitTestTwoObjectCandidate()
	store := newCommitTestStore(
		root, commitTestRandom(0xd1), filesystem, hook,
	)
	result, err, _ := commitTestCommit(t, store, candidate)
	selection := commitTestRequireCommitted(t, result, err)
	generationID := selection.Generation.GenerationID

	for _, path := range []string{
		root,
		filepath.Join(root, "generations"),
		filepath.Join(root, "objects"),
		filepath.Join(root, "generations", generationID),
	} {
		commitTestRequireMode(t, path, os.ModeDir, 0700)
	}
	for _, path := range []string{
		filepath.Join(root, "current"),
		filepath.Join(
			root, "generations", generationID, "aggregate.json",
		),
		filepath.Join(
			root, "generations", generationID, "manifest.json",
		),
		filepath.Join(
			root, "generations", generationID, "status.json",
		),
		filepath.Join(
			root, "objects",
			commitTestObjectDigest(candidate.Objects[0])+".json",
		),
		filepath.Join(
			root, "objects",
			commitTestObjectDigest(candidate.Objects[1])+".json",
		),
	} {
		commitTestRequireMode(t, path, 0, 0600)
	}

	wantNoReplace := map[string]bool{
		commitTestObjectDigest(candidate.Objects[0]) + ".json": false,
		commitTestObjectDigest(candidate.Objects[1]) + ".json": false,
		generationID: false,
	}
	var currentReplace int
	for _, operation := range filesystem.snapshot() {
		switch operation.Kind {
		case "rename_no_replace":
			if _, exists := wantNoReplace[operation.NewName]; !exists {
				t.Fatalf(
					"unexpected no-replace target: %#v", operation,
				)
			}
			wantNoReplace[operation.NewName] = true
		case "rename_replace":
			if operation.NewName != "current" {
				t.Fatalf(
					"non-current target used replace rename: %#v",
					operation,
				)
			}
			currentReplace++
		}
	}
	for target, seen := range wantNoReplace {
		if !seen {
			t.Errorf("target %q was not published no-replace", target)
		}
	}
	if currentReplace != 1 {
		t.Fatalf(
			"current replace rename count = %d, want 1",
			currentReplace,
		)
	}
}

func TestDiskCommitFsyncsEveryLayerInRequiredOrder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	filesystem := newCommitTestFilesystem()
	hook := &commitTestFaultHook{}
	candidate := commitTestTwoObjectCandidate()
	store := newCommitTestStore(
		root, commitTestRandom(0xe1), filesystem, hook,
	)
	result, err, _ := commitTestCommit(t, store, candidate)
	selection := commitTestRequireCommitted(t, result, err)
	operations := filesystem.snapshot()

	objectRename := make(map[string]int)
	generationRename := -1
	currentRename := -1
	for index, operation := range operations {
		switch {
		case operation.Kind == "rename_no_replace" &&
			strings.HasSuffix(operation.NewName, ".json"):
			objectRename[operation.NewName] = index
		case operation.Kind == "rename_no_replace" &&
			operation.NewName == selection.Generation.GenerationID:
			generationRename = index
		case operation.Kind == "rename_replace" &&
			operation.NewName == "current":
			currentRename = index
		}
	}
	if len(objectRename) != 2 ||
		generationRename < 0 ||
		currentRename < 0 {
		t.Fatalf("incomplete publish trace: %#v", operations)
	}
	firstObjectRename := len(operations)
	for _, index := range objectRename {
		if index < firstObjectRename {
			firstObjectRename = index
		}
		if index >= generationRename {
			t.Fatalf(
				"object published after generation: %#v", operations,
			)
		}
	}
	if generationRename >= currentRename {
		t.Fatalf("generation was not published before current")
	}

	for _, object := range candidate.Objects {
		objectDigest := commitTestObjectDigest(object)
		renameIndex := objectRename[objectDigest+".json"]
		renameOperation := operations[renameIndex]
		stagedPath := filepath.Join(
			renameOperation.OldDirectory,
			renameOperation.OldName,
		)
		syncIndex := commitTestFindOperation(
			operations,
			func(operation commitTestFSOperation) bool {
				return operation.Kind == "sync" &&
					!operation.IsDirectory &&
					operation.StatError == nil &&
					filepath.Clean(operation.Path) ==
						filepath.Clean(stagedPath)
			},
		)
		if syncIndex < 0 || syncIndex >= renameIndex {
			t.Fatalf(
				"object %s sync=%d rename=%d ops=%#v",
				objectDigest, syncIndex, renameIndex, operations,
			)
		}
	}

	for _, fileName := range []string{
		"aggregate.json", "manifest.json", "status.json",
	} {
		syncIndex := commitTestFindOperation(
			operations,
			func(operation commitTestFSOperation) bool {
				return operation.Kind == "sync" &&
					!operation.IsDirectory &&
					filepath.Base(operation.Path) == fileName
			},
		)
		if syncIndex < 0 || syncIndex >= firstObjectRename {
			t.Fatalf(
				"%s sync=%d first-object-rename=%d",
				fileName, syncIndex, firstObjectRename,
			)
		}
	}
	stagedGenerationSync := commitTestFindOperationBefore(
		operations,
		firstObjectRename,
		func(operation commitTestFSOperation) bool {
			return operation.Kind == "sync" &&
				operation.IsDirectory &&
				filepath.Clean(operation.Path) != filepath.Clean(root) &&
				filepath.Clean(operation.Path) !=
					filepath.Join(root, "objects") &&
				filepath.Clean(operation.Path) !=
					filepath.Join(root, "generations")
		},
	)
	if stagedGenerationSync < 0 {
		t.Fatalf(
			"staged generation directory was not synced before objects: %#v",
			operations,
		)
	}

	objectsDirectorySync := commitTestFindOperationBetween(
		operations,
		generationRename,
		currentRename,
		func(operation commitTestFSOperation) bool {
			return operation.Kind == "sync" &&
				operation.IsDirectory &&
				filepath.Clean(operation.Path) ==
					filepath.Join(root, "objects")
		},
	)
	generationsDirectorySync := commitTestFindOperationBetween(
		operations,
		generationRename,
		currentRename,
		func(operation commitTestFSOperation) bool {
			return operation.Kind == "sync" &&
				operation.IsDirectory &&
				filepath.Clean(operation.Path) ==
					filepath.Join(root, "generations")
		},
	)
	publishedGenerationSync := commitTestFindOperationBetween(
		operations,
		generationRename,
		currentRename,
		func(operation commitTestFSOperation) bool {
			clean := filepath.Clean(operation.Path)
			return operation.Kind == "sync" &&
				operation.IsDirectory &&
				clean != filepath.Clean(root) &&
				clean != filepath.Join(root, "objects") &&
				clean != filepath.Join(root, "generations")
		},
	)
	if objectsDirectorySync < 0 ||
		publishedGenerationSync < 0 ||
		generationsDirectorySync < 0 ||
		!(objectsDirectorySync < publishedGenerationSync &&
			publishedGenerationSync < generationsDirectorySync) {
		t.Fatalf(
			"post-generation directory sync order objects=%d generation=%d generations=%d ops=%#v",
			objectsDirectorySync,
			publishedGenerationSync,
			generationsDirectorySync,
			operations,
		)
	}

	currentRenameOperation := operations[currentRename]
	stagedCurrentPath := filepath.Join(
		currentRenameOperation.OldDirectory,
		currentRenameOperation.OldName,
	)
	currentFileSync := commitTestFindOperationBetween(
		operations,
		generationsDirectorySync,
		currentRename,
		func(operation commitTestFSOperation) bool {
			return operation.Kind == "sync" &&
				!operation.IsDirectory &&
				operation.StatError == nil &&
				filepath.Clean(operation.Path) ==
					filepath.Clean(stagedCurrentPath)
		},
	)
	if currentFileSync < 0 {
		t.Fatalf(
			"temporary current was not synced after generation: %#v",
			operations,
		)
	}
	rootSync := commitTestFindOperationBetween(
		operations,
		currentRename,
		len(operations),
		func(operation commitTestFSOperation) bool {
			return operation.Kind == "sync" &&
				operation.IsDirectory &&
				filepath.Clean(operation.Path) == filepath.Clean(root)
		},
	)
	if rootSync < 0 {
		t.Fatalf("state root was not synced after current rename: %#v",
			operations)
	}
}

func commitTestRequireMode(
	t *testing.T,
	path string,
	fileType os.FileMode,
	permissions os.FileMode,
) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeType != fileType ||
		info.Mode().Perm() != permissions {
		t.Fatalf(
			"%s mode = %s, want type=%s perm=%#o",
			path, info.Mode(), fileType, permissions,
		)
	}
}

func commitTestFindOperation(
	operations []commitTestFSOperation,
	match func(commitTestFSOperation) bool,
) int {
	return commitTestFindOperationBetween(
		operations, -1, len(operations), match,
	)
}

func commitTestFindOperationBefore(
	operations []commitTestFSOperation,
	before int,
	match func(commitTestFSOperation) bool,
) int {
	return commitTestFindOperationBetween(
		operations, -1, before, match,
	)
}

func commitTestFindOperationBetween(
	operations []commitTestFSOperation,
	after int,
	before int,
	match func(commitTestFSOperation) bool,
) int {
	for index := after + 1; index < before; index++ {
		if match(operations[index]) {
			return index
		}
	}
	return -1
}
