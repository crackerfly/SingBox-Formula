package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

const diskGenerationIDAttempts = 32

var (
	errDiskGenerationCollision = errors.New("generation id collision")
	diskPrivateSequence        atomic.Uint64
)

type diskGenerationStoreDependencies struct {
	GenerationRandom io.Reader
	FS               diskGenerationFilesystem
	FaultHook        func(string) error
	Legacy           legacySourceProvider
}

func newDiskGenerationStoreWithDependencies(
	root string,
	dependencies diskGenerationStoreDependencies,
) generationStore {
	generationRandom := dependencies.GenerationRandom
	if generationRandom == nil {
		generationRandom = rand.Reader
	}
	filesystem := dependencies.FS
	if filesystem == nil {
		filesystem = nativeDiskGenerationFilesystem{}
	}
	faultHook := dependencies.FaultHook
	if faultHook == nil {
		faultHook = func(string) error { return nil }
	}
	return &diskGenerationStore{
		root:             root,
		generationRandom: generationRandom,
		filesystem:       filesystem,
		faultHook:        faultHook,
		legacy:           dependencies.Legacy,
	}
}

type preparedDiskCandidate struct {
	candidate    generationCandidate
	aggregate    []byte
	objectDigest []string
	objectCount  []int
}

func (store *diskGenerationStore) Commit(
	ctx context.Context,
	candidate generationCandidate,
) (generationCommitResult, error) {
	if ctx == nil || ctx.Err() != nil || store == nil ||
		store.root == "" || store.generationRandom == nil ||
		store.filesystem == nil || store.faultHook == nil {
		return generationCommitResult{}, errDiskStateInvalid
	}
	prepared, err := prepareDiskCandidate(candidate)
	if err != nil {
		return generationCommitResult{}, errDiskStateInvalid
	}
	state, err := diskOpenOrCreateState(
		store.root, store.filesystem,
	)
	if err != nil {
		return generationCommitResult{}, errDiskStateInvalid
	}
	defer state.close()
	selection, err := diskLoadCurrentFromOpened(ctx, state)
	if err != nil || !diskCandidateParentAgrees(
		prepared, selection, store.legacy,
	) {
		return generationCommitResult{}, errDiskStateInvalid
	}

	for attempt := 0; attempt < diskGenerationIDAttempts; attempt++ {
		if ctx.Err() != nil {
			return generationCommitResult{}, errDiskStateInvalid
		}
		generationID, err := store.nextUnusedGenerationID(
			state.generations,
		)
		if err != nil {
			return generationCommitResult{}, errDiskStateInvalid
		}
		result, err := store.commitPreparedGeneration(
			ctx, state, prepared, generationID,
		)
		if errors.Is(err, errDiskGenerationCollision) {
			continue
		}
		return result, err
	}
	return generationCommitResult{}, errDiskStateInvalid
}

func diskCandidateParentAgrees(
	prepared preparedDiskCandidate,
	selection currentSelection,
	legacy legacySourceProvider,
) bool {
	if !diskLegacyConsumptionAgrees(
		prepared, selection, legacy,
	) {
		return false
	}
	switch selection.Kind {
	case currentAbsent:
		if prepared.candidate.ParentGenerationID != "" {
			return false
		}
		for _, source := range prepared.candidate.Sources {
			if source.Result == sourceResultFallback &&
				!diskIsLegacyFallback(prepared, source) {
				return false
			}
		}
		return true
	case currentPresent:
		if prepared.candidate.ParentGenerationID !=
			selection.Generation.GenerationID {
			return false
		}
		parentSources, valid := fallbackSources(
			&selection.Generation,
		)
		if !valid {
			return false
		}
		for _, source := range prepared.candidate.Sources {
			if source.Result != sourceResultFallback {
				continue
			}
			parent, exists := parentSources[source.URLDigest]
			if !exists {
				if diskIsLegacyFallback(prepared, source) &&
					selection.Generation.
						LegacyConsumedURLDigest == "" {
					continue
				}
				return false
			}
			objectOffset := source.ObjectIndex - 1
			object := prepared.candidate.Objects[objectOffset]
			if parent.ObjectDigest !=
				prepared.objectDigest[objectOffset] ||
				!bytes.Equal(parent.Normalized, object) ||
				!equalNormalizeInfo(parent.Info, source.Info) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func diskLegacyConsumptionAgrees(
	prepared preparedDiskCandidate,
	selection currentSelection,
	legacy legacySourceProvider,
) bool {
	consumed := prepared.candidate.LegacyConsumedURLDigest
	receipt := prepared.candidate.legacyMarkerReceipt
	parentConsumed := ""
	switch selection.Kind {
	case currentAbsent:
	case currentPresent:
		parentConsumed =
			selection.Generation.LegacyConsumedURLDigest
	default:
		return false
	}
	if consumed == "" {
		return receipt == nil && parentConsumed == ""
	}
	if !isLowerHexDigest(consumed) ||
		(parentConsumed != "" && parentConsumed != consumed) {
		return false
	}
	if receipt == nil {
		return parentConsumed == consumed
	}
	return legacy != nil &&
		len(prepared.candidate.Sources) > 0 &&
		prepared.candidate.Sources[0].URLDigest == consumed &&
		receipt.matchesDigest(consumed)
}

func diskIsLegacyFallback(
	prepared preparedDiskCandidate,
	source generationCandidateSource,
) bool {
	return source.Index == 1 &&
		source.Result == sourceResultFallback &&
		source.Info.Format == FormatSingBoxJSON &&
		prepared.candidate.legacyMarkerReceipt != nil &&
		prepared.candidate.LegacyConsumedURLDigest != "" &&
		source.URLDigest ==
			prepared.candidate.LegacyConsumedURLDigest
}

func prepareDiskCandidate(
	input generationCandidate,
) (preparedDiskCandidate, error) {
	if !isLowerHexDigest(input.ConfigDigest) ||
		(input.ParentGenerationID != "" &&
			!isLowerHexDigest(input.ParentGenerationID)) ||
		(input.LegacyConsumedURLDigest != "" &&
			!isLowerHexDigest(
				input.LegacyConsumedURLDigest,
			)) ||
		(input.LegacyConsumedURLDigest == "" &&
			input.legacyMarkerReceipt != nil) ||
		len(input.Objects) < 1 ||
		len(input.Objects) > 8 ||
		len(input.Sources) < 1 ||
		len(input.Sources) > 8 {
		return preparedDiskCandidate{}, errDiskStateInvalid
	}

	canonicalObjects := make([][]byte, 0, len(input.Objects))
	objectDigests := make([]string, 0, len(input.Objects))
	objectCounts := make([]int, 0, len(input.Objects))
	oldToNew := make([]int, len(input.Objects))
	digestIndex := make(map[string]int)
	for offset, object := range input.Objects {
		canonical, count, err :=
			canonicalizeStoredSource(object)
		if err != nil || !bytes.Equal(canonical, object) {
			return preparedDiskCandidate{}, errDiskStateInvalid
		}
		digest := diskSHA256(canonical)
		if previous, exists := digestIndex[digest]; exists {
			if !bytes.Equal(
				canonicalObjects[previous-1], canonical,
			) {
				return preparedDiskCandidate{}, errDiskStateInvalid
			}
			oldToNew[offset] = previous
			continue
		}
		canonicalObjects = append(canonicalObjects, canonical)
		objectDigests = append(objectDigests, digest)
		objectCounts = append(objectCounts, count)
		index := len(canonicalObjects)
		digestIndex[digest] = index
		oldToNew[offset] = index
	}

	var receipt *legacyReadToken
	if input.legacyMarkerReceipt != nil {
		copyReceipt := *input.legacyMarkerReceipt
		receipt = &copyReceipt
	}
	candidate := generationCandidate{
		ParentGenerationID:      input.ParentGenerationID,
		ConfigDigest:            input.ConfigDigest,
		State:                   input.State,
		LegacyConsumedURLDigest: input.LegacyConsumedURLDigest,
		legacyMarkerReceipt:     receipt,
		Objects:                 canonicalObjects,
		Sources: make(
			[]generationCandidateSource, len(input.Sources),
		),
	}
	referenced := make([]bool, len(canonicalObjects))
	fallbackCount := 0
	type duplicateMetadata struct {
		objectIndex int
		result      string
		fetchCode   sourceFetchCode
		info        NormalizeInfo
	}
	duplicates := make(map[string]duplicateMetadata)
	for offset, source := range input.Sources {
		if source.Index != offset+1 ||
			source.ObjectIndex < 1 ||
			source.ObjectIndex > len(oldToNew) ||
			!isLowerHexDigest(source.URLDigest) {
			return preparedDiskCandidate{}, errDiskStateInvalid
		}
		source.ObjectIndex = oldToNew[source.ObjectIndex-1]
		referenced[source.ObjectIndex-1] = true
		objectCount := objectCounts[source.ObjectIndex-1]
		statusSource := diskStatusSource{
			Index:     source.Index,
			Result:    source.Result,
			FetchCode: string(source.FetchCode),
			Format:    string(source.Info.Format),
			Accepted:  source.Info.Accepted,
			Skipped:   source.Info.Skipped,
			Warnings:  diskWarningsFromInfo(source.Info),
		}
		info, err := diskValidateStatusSource(
			statusSource, objectCount,
		)
		if err != nil {
			return preparedDiskCandidate{}, errDiskStateInvalid
		}
		source.Info = info
		switch source.Result {
		case sourceResultFresh:
			if source.FetchCode != fetchCodeOK {
				return preparedDiskCandidate{}, errDiskStateInvalid
			}
		case sourceResultFallback:
			if !validFetchFailureCode(source.FetchCode) {
				return preparedDiskCandidate{}, errDiskStateInvalid
			}
			fallbackCount++
		default:
			return preparedDiskCandidate{}, errDiskStateInvalid
		}
		metadata := duplicateMetadata{
			objectIndex: source.ObjectIndex,
			result:      source.Result,
			fetchCode:   source.FetchCode,
			info:        info,
		}
		if previous, exists := duplicates[source.URLDigest]; exists {
			if previous.objectIndex != metadata.objectIndex ||
				previous.result != metadata.result ||
				previous.fetchCode != metadata.fetchCode ||
				!equalNormalizeInfo(previous.info, metadata.info) {
				return preparedDiskCandidate{}, errDiskStateInvalid
			}
		} else {
			duplicates[source.URLDigest] = metadata
		}
		candidate.Sources[offset] = source
	}
	for _, isReferenced := range referenced {
		if !isReferenced {
			return preparedDiskCandidate{}, errDiskStateInvalid
		}
	}
	if fallbackCount == 0 {
		if candidate.State != generationStateFresh {
			return preparedDiskCandidate{}, errDiskStateInvalid
		}
	} else if candidate.State != generationStateDegraded {
		return preparedDiskCandidate{}, errDiskStateInvalid
	}

	aggregate, err := diskMergeCandidate(candidate)
	if err != nil {
		return preparedDiskCandidate{}, errDiskStateInvalid
	}
	canonicalAggregate, aggregateCount, err :=
		canonicalizeStoredSource(aggregate)
	if err != nil || aggregateCount < 1 ||
		!bytes.Equal(canonicalAggregate, aggregate) {
		return preparedDiskCandidate{}, errDiskStateInvalid
	}
	return preparedDiskCandidate{
		candidate:    candidate,
		aggregate:    aggregate,
		objectDigest: objectDigests,
		objectCount:  objectCounts,
	}, nil
}

// Keep the merge dependency at one narrow boundary. The canonical slice may
// replace its internal representation without changing the disk transaction.
func diskMergeCandidate(
	candidate generationCandidate,
) ([]byte, error) {
	return mergeCanonicalAggregate(candidate)
}

func diskWarningsFromInfo(info NormalizeInfo) []diskStatusWarning {
	warnings := make([]diskStatusWarning, len(info.Warnings))
	for index, warning := range info.Warnings {
		warnings[index] = diskStatusWarning{
			Code:      warning.Code,
			NodeIndex: warning.NodeIndex,
			Type:      warning.Type,
			Field:     warning.Field,
		}
	}
	return warnings
}

func diskOpenOrCreateState(
	root string,
	filesystem diskGenerationFilesystem,
) (*diskOpenedState, error) {
	cleanRoot := filepath.Clean(root)
	parentPath := filepath.Dir(cleanRoot)
	rootName := filepath.Base(cleanRoot)
	if !filepath.IsAbs(cleanRoot) ||
		rootName == "." || rootName == string(os.PathSeparator) ||
		strings.Contains(rootName, string(os.PathSeparator)) ||
		filesystem == nil {
		return nil, errDiskStateInvalid
	}
	parent, err := diskOpenUnrestrictedDirectoryPath(parentPath)
	if err != nil {
		return nil, errDiskStateInvalid
	}
	defer parent.Close()
	createdRoot := false
	if err := unix.Mkdirat(
		int(parent.Fd()), rootName, 0700,
	); err == nil {
		createdRoot = true
	} else if !errors.Is(err, unix.EEXIST) {
		return nil, errDiskStateInvalid
	}
	rootFile, err := diskOpenDirectoryAtUnchecked(parent, rootName)
	if err != nil {
		return nil, errDiskStateInvalid
	}
	if createdRoot {
		if unix.Fchmod(int(rootFile.Fd()), 0700) != nil ||
			!diskValidDirectory(rootFile) {
			_ = rootFile.Close()
			return nil, errDiskStateInvalid
		}
	} else if !diskValidDirectory(rootFile) {
		_ = rootFile.Close()
		return nil, errDiskStateInvalid
	}
	// Always resync the parent. A prior attempt may have created the root but
	// failed this durability barrier before it could publish current.
	if filesystem.Sync(parent) != nil {
		_ = rootFile.Close()
		return nil, errDiskStateInvalid
	}
	state := &diskOpenedState{root: rootFile}
	generations, err := diskOpenOrCreateDirectoryAt(
		rootFile, "generations",
	)
	if err != nil {
		state.close()
		return nil, errDiskStateInvalid
	}
	state.generations = generations
	objects, err := diskOpenOrCreateDirectoryAt(rootFile, "objects")
	if err != nil {
		state.close()
		return nil, errDiskStateInvalid
	}
	state.objects = objects
	return state, nil
}

func diskOpenUnrestrictedDirectoryPath(
	path string,
) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = file.Close()
		return nil, errDiskStateInvalid
	}
	return file, nil
}

func diskOpenOrCreateDirectoryAt(
	parent *os.File,
	name string,
) (*os.File, error) {
	created := false
	if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	directory, err := diskOpenDirectoryAtUnchecked(parent, name)
	if err != nil {
		return nil, err
	}
	if created {
		if unix.Fchmod(int(directory.Fd()), 0700) != nil ||
			!diskValidDirectory(directory) {
			_ = directory.Close()
			return nil, errDiskStateInvalid
		}
	} else if !diskValidDirectory(directory) {
		_ = directory.Close()
		return nil, errDiskStateInvalid
	}
	return directory, nil
}

func diskLoadCurrentFromOpened(
	ctx context.Context,
	state *diskOpenedState,
) (currentSelection, error) {
	if ctx == nil || ctx.Err() != nil || state == nil ||
		state.root == nil || state.generations == nil ||
		state.objects == nil {
		return currentSelection{Kind: currentInvalid}, errDiskStateInvalid
	}
	current, missing, err := diskReadSecureFileAt(
		state.root, "current", diskCurrentBytes,
	)
	if err != nil {
		return currentSelection{Kind: currentInvalid}, err
	}
	if missing {
		return currentSelection{Kind: currentAbsent}, nil
	}
	if len(current) != diskCurrentBytes ||
		current[diskCurrentBytes-1] != '\n' {
		return currentSelection{Kind: currentInvalid}, errDiskStateInvalid
	}
	generationID := string(current[:diskCurrentBytes-1])
	if !isLowerHexDigest(generationID) {
		return currentSelection{Kind: currentInvalid}, errDiskStateInvalid
	}
	generation, err := diskLoadGeneration(
		ctx, state.generations, state.objects, generationID,
	)
	if err != nil {
		return currentSelection{Kind: currentInvalid}, err
	}
	return currentSelection{
		Kind: currentPresent, Generation: generation,
	}, nil
}

func (store *diskGenerationStore) nextUnusedGenerationID(
	generations *os.File,
) (string, error) {
	for attempt := 0; attempt < diskGenerationIDAttempts; attempt++ {
		bytes := make([]byte, 32)
		if _, err := io.ReadFull(store.generationRandom, bytes); err != nil {
			return "", err
		}
		generationID := hex.EncodeToString(bytes)
		var stat unix.Stat_t
		err := unix.Fstatat(
			int(generations.Fd()), generationID, &stat,
			unix.AT_SYMLINK_NOFOLLOW,
		)
		if errors.Is(err, unix.ENOENT) {
			return generationID, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errDiskGenerationCollision
}

func (store *diskGenerationStore) commitPreparedGeneration(
	ctx context.Context,
	state *diskOpenedState,
	prepared preparedDiskCandidate,
	generationID string,
) (generationCommitResult, error) {
	manifest, status, manifestRaw, statusRaw, err :=
		diskBuildGenerationMetadata(prepared, generationID)
	if err != nil {
		return generationCommitResult{}, errDiskStateInvalid
	}
	stagingName, staging, err := diskCreatePrivateDirectory(
		state.root,
	)
	if err != nil {
		return generationCommitResult{}, errDiskStateInvalid
	}
	generationPublished := false
	logicalCommitted := false
	defer func() {
		_ = staging.Close()
		if !generationPublished {
			diskRemovePrivateDirectory(state.root, stagingName)
		} else if !logicalCommitted {
			store.diskRemoveUnpublishedGeneration(
				state, generationID,
			)
		}
	}()

	stagedObjectNames := make([]string, len(prepared.candidate.Objects))
	for index, object := range prepared.candidate.Objects {
		name := ".object-" + prepared.objectDigest[index] + ".json"
		if diskWriteAndSyncFile(
			staging, name, object, store.filesystem,
		) != nil {
			return generationCommitResult{}, errDiskStateInvalid
		}
		stagedObjectNames[index] = name
	}
	for _, file := range []struct {
		name     string
		contents []byte
	}{
		{name: "aggregate.json", contents: prepared.aggregate},
		{name: "manifest.json", contents: manifestRaw},
		{name: "status.json", contents: statusRaw},
	} {
		if diskWriteAndSyncFile(
			staging, file.name, file.contents, store.filesystem,
		) != nil {
			return generationCommitResult{}, errDiskStateInvalid
		}
	}
	if store.filesystem.Sync(staging) != nil {
		return generationCommitResult{}, errDiskStateInvalid
	}

	for index, digest := range prepared.objectDigest {
		if ctx.Err() != nil ||
			store.faultHook(
				"before_object_rename:"+digest,
			) != nil {
			return generationCommitResult{}, errDiskStateInvalid
		}
		targetName := digest + ".json"
		err := store.filesystem.RenameNoReplace(
			staging, stagedObjectNames[index],
			state.objects, targetName,
		)
		if errors.Is(err, unix.EEXIST) {
			existing, missing, readErr := diskReadSecureFileAt(
				state.objects, targetName,
				canonicalAggregateByteLimit,
			)
			if readErr != nil || missing ||
				!bytes.Equal(
					existing, prepared.candidate.Objects[index],
				) ||
				diskSHA256(existing) != digest {
				return generationCommitResult{},
					errDiskStateInvalid
			}
			if unix.Unlinkat(
				int(staging.Fd()), stagedObjectNames[index], 0,
			) != nil {
				return generationCommitResult{},
					errDiskStateInvalid
			}
		} else if err != nil {
			return generationCommitResult{}, errDiskStateInvalid
		}
		published, missing, readErr := diskReadSecureFileAt(
			state.objects, targetName, canonicalAggregateByteLimit,
		)
		if readErr != nil || missing ||
			!bytes.Equal(
				published, prepared.candidate.Objects[index],
			) ||
			diskSHA256(published) != digest {
			return generationCommitResult{}, errDiskStateInvalid
		}
	}
	if store.filesystem.Sync(staging) != nil ||
		store.faultHook("before_generation_rename") != nil ||
		!diskGenerationDirectoryMatches(
			staging, prepared.aggregate, manifestRaw, statusRaw,
		) {
		return generationCommitResult{}, errDiskStateInvalid
	}
	err = store.filesystem.RenameNoReplace(
		state.root, stagingName,
		state.generations, generationID,
	)
	if errors.Is(err, unix.EEXIST) {
		return generationCommitResult{}, errDiskGenerationCollision
	}
	if err != nil {
		return generationCommitResult{}, errDiskStateInvalid
	}
	generationPublished = true

	if store.filesystem.Sync(state.objects) != nil ||
		store.filesystem.Sync(staging) != nil ||
		store.filesystem.Sync(state.generations) != nil {
		return generationCommitResult{}, errDiskStateInvalid
	}
	currentTemporaryName := diskPrivateName(".current-")
	if diskWriteAndSyncFile(
		state.root, currentTemporaryName,
		[]byte(generationID+"\n"), store.filesystem,
	) != nil {
		return generationCommitResult{}, errDiskStateInvalid
	}
	currentTemporaryPresent := true
	defer func() {
		if currentTemporaryPresent {
			_ = unix.Unlinkat(
				int(state.root.Fd()), currentTemporaryName, 0,
			)
		}
	}()
	if ctx.Err() != nil ||
		store.faultHook("before_current_rename") != nil {
		return generationCommitResult{}, errDiskStateInvalid
	}
	publishedDirectory, err := diskOpenDirectoryAt(
		state.generations, generationID,
	)
	if err != nil {
		return generationCommitResult{}, errDiskStateInvalid
	}
	publishedMatches := diskGenerationDirectoryMatches(
		publishedDirectory,
		prepared.aggregate,
		manifestRaw,
		statusRaw,
	)
	_ = publishedDirectory.Close()
	if !publishedMatches {
		return generationCommitResult{}, errDiskStateInvalid
	}
	currentBytes, currentMissing, currentErr :=
		diskReadSecureFileAt(
			state.root, currentTemporaryName, diskCurrentBytes,
		)
	if currentErr != nil || currentMissing ||
		!bytes.Equal(
			currentBytes, []byte(generationID+"\n"),
		) {
		return generationCommitResult{}, errDiskStateInvalid
	}
	revalidated, err := diskLoadGeneration(
		ctx, state.generations, state.objects, generationID,
	)
	if err != nil ||
		!diskPreparedGenerationMatches(
			prepared, generationID, revalidated,
		) ||
		!beginCurrentCommit(ctx) {
		return generationCommitResult{}, errDiskStateInvalid
	}
	if store.filesystem.RenameReplace(
		state.root, currentTemporaryName,
		state.root, "current",
	) != nil {
		return generationCommitResult{}, errDiskStateInvalid
	}
	currentTemporaryPresent = false
	logicalCommitted = true

	selection := diskSelectionFromPrepared(
		prepared, manifest, status, generationID,
	)
	result := generationCommitResult{
		Committed: true,
		Selection: currentSelection{
			Kind: currentPresent, Generation: selection,
		},
	}
	if store.faultHook(
		"after_current_rename_before_root_sync",
	) != nil {
		result.WarningCode = "current_dir_sync_failed"
		return result, nil
	}
	if store.filesystem.Sync(state.root) != nil {
		result.WarningCode = "current_dir_sync_failed"
		return result, nil
	}
	if prepared.candidate.legacyMarkerReceipt != nil {
		if store.legacy == nil ||
			store.legacy.RemoveCommittedMarker(
				ctx,
				*prepared.candidate.legacyMarkerReceipt,
				prepared.candidate.LegacyConsumedURLDigest,
			) != nil {
			result.WarningCode = "legacy_marker_cleanup_failed"
		}
	}
	if store.faultHook("before_gc") == nil {
		store.runGenerationGC(ctx, state, generationID)
	}
	return result, nil
}

func diskBuildGenerationMetadata(
	prepared preparedDiskCandidate,
	generationID string,
) (
	diskManifest,
	diskStatus,
	[]byte,
	[]byte,
	error,
) {
	aggregateCounted, aggregateCount, err :=
		canonicalizeStoredSource(prepared.aggregate)
	if err != nil ||
		!bytes.Equal(aggregateCounted, prepared.aggregate) {
		return diskManifest{}, diskStatus{}, nil, nil,
			errDiskStateInvalid
	}
	manifest := diskManifest{
		Schema:       1,
		Generation:   generationID,
		Parent:       prepared.candidate.ParentGenerationID,
		ConfigDigest: prepared.candidate.ConfigDigest,
		Aggregate: diskAggregateMetadata{
			SHA256:    diskSHA256(prepared.aggregate),
			Bytes:     len(prepared.aggregate),
			Outbounds: aggregateCount,
		},
		Sources: make(
			[]diskManifestSource,
			len(prepared.candidate.Sources),
		),
		LegacyConsumedURLSHA256: prepared.candidate.
			LegacyConsumedURLDigest,
	}
	status := diskStatus{
		Schema:          1,
		Generation:      generationID,
		State:           prepared.candidate.State,
		FallbackIndices: []int{},
		Sources: make(
			[]diskStatusSource,
			len(prepared.candidate.Sources),
		),
	}
	for offset, source := range prepared.candidate.Sources {
		objectOffset := source.ObjectIndex - 1
		manifest.Sources[offset] = diskManifestSource{
			Index:        source.Index,
			URLSHA256:    source.URLDigest,
			ObjectSHA256: prepared.objectDigest[objectOffset],
			Bytes: len(
				prepared.candidate.Objects[objectOffset],
			),
			Outbounds: prepared.objectCount[objectOffset],
		}
		status.Sources[offset] = diskStatusSource{
			Index:     source.Index,
			Result:    source.Result,
			FetchCode: string(source.FetchCode),
			Format:    string(source.Info.Format),
			Accepted:  source.Info.Accepted,
			Skipped:   source.Info.Skipped,
			Warnings:  diskWarningsFromInfo(source.Info),
		}
		if source.Result == sourceResultFresh {
			status.FreshCount++
		} else {
			status.FallbackIndices = append(
				status.FallbackIndices, source.Index,
			)
		}
	}
	statusRaw, err := json.Marshal(status)
	if err != nil || len(statusRaw) > diskStateSchemaLimit {
		return diskManifest{}, diskStatus{}, nil, nil,
			errDiskStateInvalid
	}
	manifest.StatusSHA256 = diskSHA256(statusRaw)
	manifestRaw, err := json.Marshal(manifest)
	if err != nil || len(manifestRaw) > diskStateSchemaLimit {
		return diskManifest{}, diskStatus{}, nil, nil,
			errDiskStateInvalid
	}
	return manifest, status, manifestRaw, statusRaw, nil
}

func diskSelectionFromPrepared(
	prepared preparedDiskCandidate,
	_ diskManifest,
	_ diskStatus,
	generationID string,
) validatedGeneration {
	sources := make(
		[]generationSource, len(prepared.candidate.Sources),
	)
	for offset, source := range prepared.candidate.Sources {
		objectOffset := source.ObjectIndex - 1
		sources[offset] = generationSource{
			Index:        source.Index,
			URLDigest:    source.URLDigest,
			ObjectDigest: prepared.objectDigest[objectOffset],
			Normalized:   prepared.candidate.Objects[objectOffset],
			Info:         cloneNormalizeInfo(source.Info),
		}
	}
	return validatedGeneration{
		GenerationID: generationID,
		ConfigDigest: prepared.candidate.ConfigDigest,
		Aggregate:    prepared.aggregate,
		Sources:      sources,
		LegacyConsumedURLDigest: prepared.candidate.
			LegacyConsumedURLDigest,
	}
}

func diskGenerationDirectoryMatches(
	directory *os.File,
	aggregate []byte,
	manifest []byte,
	status []byte,
) bool {
	if !diskGenerationHasExactFiles(directory) {
		return false
	}
	for _, expected := range []struct {
		name     string
		contents []byte
		limit    int
	}{
		{
			name: "aggregate.json", contents: aggregate,
			limit: canonicalAggregateByteLimit,
		},
		{
			name: "manifest.json", contents: manifest,
			limit: diskStateSchemaLimit,
		},
		{
			name: "status.json", contents: status,
			limit: diskStateSchemaLimit,
		},
	} {
		contents, missing, err := diskReadSecureFileAt(
			directory, expected.name, expected.limit,
		)
		if err != nil || missing ||
			!bytes.Equal(contents, expected.contents) {
			return false
		}
	}
	return true
}

func diskPreparedGenerationMatches(
	prepared preparedDiskCandidate,
	generationID string,
	generation validatedGeneration,
) bool {
	if generation.GenerationID != generationID ||
		generation.ConfigDigest != prepared.candidate.ConfigDigest ||
		generation.LegacyConsumedURLDigest !=
			prepared.candidate.LegacyConsumedURLDigest ||
		!bytes.Equal(generation.Aggregate, prepared.aggregate) ||
		len(generation.Sources) != len(prepared.candidate.Sources) {
		return false
	}
	for offset, expected := range prepared.candidate.Sources {
		actual := generation.Sources[offset]
		objectOffset := expected.ObjectIndex - 1
		if actual.Index != expected.Index ||
			actual.URLDigest != expected.URLDigest ||
			actual.ObjectDigest !=
				prepared.objectDigest[objectOffset] ||
			!bytes.Equal(
				actual.Normalized,
				prepared.candidate.Objects[objectOffset],
			) ||
			!equalNormalizeInfo(actual.Info, expected.Info) {
			return false
		}
	}
	return true
}

func diskCreatePrivateDirectory(
	root *os.File,
) (string, *os.File, error) {
	for attempt := 0; attempt < diskGenerationIDAttempts; attempt++ {
		name := diskPrivateName(".staging-")
		err := unix.Mkdirat(int(root.Fd()), name, 0700)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		directory, err := diskOpenDirectoryAtUnchecked(root, name)
		if err != nil {
			_ = unix.Unlinkat(
				int(root.Fd()), name, unix.AT_REMOVEDIR,
			)
			return "", nil, err
		}
		if unix.Fchmod(int(directory.Fd()), 0700) != nil ||
			!diskValidDirectory(directory) {
			_ = directory.Close()
			_ = unix.Unlinkat(
				int(root.Fd()), name, unix.AT_REMOVEDIR,
			)
			return "", nil, errDiskStateInvalid
		}
		return name, directory, nil
	}
	return "", nil, errDiskStateInvalid
}

func diskPrivateName(prefix string) string {
	sequence := diskPrivateSequence.Add(1)
	return fmt.Sprintf("%s%d-%016x", prefix, os.Getpid(), sequence)
}

func diskWriteAndSyncFile(
	directory *os.File,
	name string,
	contents []byte,
	filesystem diskGenerationFilesystem,
) error {
	fd, err := unix.Openat(
		int(directory.Fd()), name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|
			unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0600,
	)
	if err != nil {
		return err
	}
	path := filepath.Join(directory.Name(), name)
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	if unix.Fchmod(fd, 0600) != nil {
		return errDiskStateInvalid
	}
	written := 0
	for written < len(contents) {
		count, err := file.Write(contents[written:])
		if err != nil || count <= 0 {
			return errDiskStateInvalid
		}
		written += count
	}
	if filesystem.Sync(file) != nil {
		return errDiskStateInvalid
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&07777 != 0600 ||
		stat.Nlink != 1 ||
		stat.Size != int64(len(contents)) {
		return errDiskStateInvalid
	}
	return nil
}

func diskRemovePrivateDirectory(
	root *os.File,
	name string,
) {
	directory, err := diskOpenDirectoryAt(root, name)
	if err != nil {
		return
	}
	duplicateFD, err := unix.Dup(int(directory.Fd()))
	if err == nil {
		copyDirectory := os.NewFile(
			uintptr(duplicateFD), directory.Name(),
		)
		entries, readErr := copyDirectory.ReadDir(-1)
		_ = copyDirectory.Close()
		if readErr == nil {
			for _, entry := range entries {
				if !entry.IsDir() &&
					!strings.Contains(entry.Name(), "/") {
					_ = unix.Unlinkat(
						int(directory.Fd()), entry.Name(), 0,
					)
				}
			}
		}
	}
	_ = directory.Close()
	_ = unix.Unlinkat(int(root.Fd()), name, unix.AT_REMOVEDIR)
}

func (store *diskGenerationStore) diskRemoveUnpublishedGeneration(
	state *diskOpenedState,
	generationID string,
) {
	if store == nil || store.filesystem == nil || state == nil ||
		state.root == nil || state.generations == nil ||
		!isLowerHexDigest(generationID) {
		return
	}

	quarantineName := ""
	for attempt := 0; attempt < diskGenerationIDAttempts; attempt++ {
		name := diskPrivateName(".cleanup-")
		err := store.filesystem.RenameNoReplace(
			state.generations, generationID,
			state.root, name,
		)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return
		}
		quarantineName = name
		break
	}
	if quarantineName == "" {
		return
	}

	// The generation remains byte-for-byte recoverable until both sides of
	// the atomic quarantine rename are durable. Only root-private quarantine
	// state may become partial after this boundary; generations never does.
	if store.filesystem.Sync(state.generations) != nil ||
		store.filesystem.Sync(state.root) != nil {
		return
	}
	if !diskRemoveQuarantinedGeneration(
		state.root, quarantineName,
	) {
		return
	}
	_ = store.filesystem.Sync(state.root)
}

func diskRemoveQuarantinedGeneration(
	root *os.File,
	quarantineName string,
) bool {
	if root == nil || !strings.HasPrefix(
		quarantineName, ".cleanup-",
	) || strings.Contains(quarantineName, "/") {
		return false
	}
	directory, err := diskOpenDirectoryAt(root, quarantineName)
	if err != nil {
		return false
	}
	for _, name := range []string{
		"aggregate.json", "manifest.json", "status.json",
	} {
		err := unix.Unlinkat(int(directory.Fd()), name, 0)
		if err != nil && !errors.Is(err, unix.ENOENT) {
			_ = directory.Close()
			return false
		}
	}
	_ = directory.Close()
	return unix.Unlinkat(
		int(root.Fd()), quarantineName, unix.AT_REMOVEDIR,
	) == nil
}
