package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type diskGCGenerationSnapshot struct {
	aggregate []byte
	manifest  []byte
	status    []byte
}

type diskGCFileIdentity struct {
	device uint64
	inode  uint64
	mode   uint32
	nlink  uint64
	size   int64
	digest string
}

type diskGCGenerationIdentity struct {
	generationID string
	parentID     string
	directory    diskGCFileIdentity
	aggregate    diskGCFileIdentity
	manifest     diskGCFileIdentity
	status       diskGCFileIdentity
}

type diskGCObjectIdentity struct {
	name string
	file diskGCFileIdentity
}

type diskGCPlan struct {
	retained      map[string]diskGCGenerationIdentity
	markedObjects map[string]bool
	generations   []diskGCGenerationIdentity
	objects       []diskGCObjectIdentity
}

const (
	diskGCGenerationQuarantinePrefix = ".gc-generation-"
	diskGCObjectQuarantinePrefix     = ".gc-object-"
)

func (store *diskGenerationStore) runGenerationGC(
	ctx context.Context,
	state *diskOpenedState,
	currentGenerationID string,
) {
	remover, ok := store.filesystem.(diskGenerationRemoveFilesystem)
	if !ok || ctx == nil || ctx.Err() != nil || state == nil ||
		state.root == nil || state.generations == nil ||
		state.objects == nil ||
		!isLowerHexDigest(currentGenerationID) {
		return
	}
	if !diskRecoverGCQuarantines(
		ctx,
		state,
		currentGenerationID,
		store.filesystem,
		remover,
	) {
		return
	}
	plan, valid := diskBuildGCPlan(
		ctx, state, currentGenerationID,
	)
	if !valid {
		return
	}

	for _, generation := range plan.generations {
		if ctx.Err() != nil ||
			!diskQuarantineAndRemoveGCGeneration(
				ctx,
				state,
				generation,
				store.filesystem,
				remover,
			) {
			return
		}
	}
	if !diskGCRetainedStillMatches(
		ctx, state, currentGenerationID, plan.retained,
	) {
		return
	}

	for _, object := range plan.objects {
		if ctx.Err() != nil ||
			!diskQuarantineAndRemoveGCObject(
				state.objects,
				object,
				store.filesystem,
				remover,
			) {
			return
		}
	}
}

func diskBuildGCPlan(
	ctx context.Context,
	state *diskOpenedState,
	currentGenerationID string,
) (diskGCPlan, bool) {
	plan := diskGCPlan{
		retained:      make(map[string]diskGCGenerationIdentity),
		markedObjects: make(map[string]bool),
	}
	selection, err := diskLoadCurrentFromOpened(ctx, state)
	if err != nil || selection.Kind != currentPresent ||
		selection.Generation.GenerationID != currentGenerationID {
		return diskGCPlan{}, false
	}

	generationParents := make(map[string]string)
	nextGenerationID := currentGenerationID
	for depth := 0; depth < 3; depth++ {
		if _, repeated := plan.retained[nextGenerationID]; repeated {
			return diskGCPlan{}, false
		}
		identity, generation, valid := diskInspectGCGeneration(
			ctx, state, nextGenerationID,
		)
		if !valid {
			return diskGCPlan{}, false
		}
		plan.retained[nextGenerationID] = identity
		generationParents[nextGenerationID] = identity.parentID
		for _, source := range generation.Sources {
			plan.markedObjects[source.ObjectDigest] = true
		}
		if identity.parentID == "" {
			break
		}
		if _, repeated := plan.retained[identity.parentID]; repeated {
			return diskGCPlan{}, false
		}
		if depth == 2 {
			break
		}
		nextGenerationID = identity.parentID
	}

	generationNames, valid := diskGCEntryNames(state.generations)
	if !valid {
		return diskGCPlan{}, false
	}
	for _, generationID := range generationNames {
		if !isLowerHexDigest(generationID) {
			continue
		}
		stat, statValid := diskGCLstatAt(
			state.generations, generationID,
		)
		if !statValid {
			return diskGCPlan{}, false
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		if stat.Mode&07777 != 0700 {
			return diskGCPlan{}, false
		}
		if _, retained := plan.retained[generationID]; retained {
			continue
		}
		identity, _, snapshotValid := diskInspectGCGeneration(
			ctx, state, generationID,
		)
		if !snapshotValid {
			return diskGCPlan{}, false
		}
		generationParents[generationID] = identity.parentID
		plan.generations = append(plan.generations, identity)
	}
	if !diskGCParentGraphAcyclic(generationParents) {
		return diskGCPlan{}, false
	}

	objectNames, valid := diskGCEntryNames(state.objects)
	if !valid {
		return diskGCPlan{}, false
	}
	for _, name := range objectNames {
		digest, standard := diskGCObjectDigest(name)
		if !standard {
			continue
		}
		stat, statValid := diskGCLstatAt(state.objects, name)
		if !statValid {
			return diskGCPlan{}, false
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG {
			continue
		}
		if stat.Mode&07777 != 0600 || stat.Nlink != 1 {
			return diskGCPlan{}, false
		}
		contents, identity, readValid := diskGCReadSecureFileAt(
			state.objects, name, canonicalAggregateByteLimit,
		)
		if !readValid || identity.digest != digest {
			return diskGCPlan{}, false
		}
		canonical, _, canonicalErr :=
			canonicalizeStoredSource(contents)
		if canonicalErr != nil || !bytes.Equal(canonical, contents) {
			return diskGCPlan{}, false
		}
		if plan.markedObjects[digest] {
			continue
		}
		plan.objects = append(plan.objects, diskGCObjectIdentity{
			name: name, file: identity,
		})
		contents = nil
	}
	return plan, true
}

func diskGCParentGraphAcyclic(parents map[string]string) bool {
	const (
		diskGCParentVisiting = 1
		diskGCParentDone     = 2
	)
	states := make(map[string]uint8, len(parents))
	for start := range parents {
		if states[start] == diskGCParentDone {
			continue
		}
		path := make([]string, 0, 4)
		generationID := start
		for generationID != "" {
			switch states[generationID] {
			case diskGCParentVisiting:
				return false
			case diskGCParentDone:
				generationID = ""
				continue
			}
			states[generationID] = diskGCParentVisiting
			path = append(path, generationID)
			parent, exists := parents[generationID]
			if !exists {
				break
			}
			generationID = parent
		}
		for _, visited := range path {
			states[visited] = diskGCParentDone
		}
	}
	return true
}

func diskInspectGCGeneration(
	ctx context.Context,
	state *diskOpenedState,
	generationID string,
) (
	diskGCGenerationIdentity,
	validatedGeneration,
	bool,
) {
	if ctx == nil || ctx.Err() != nil || state == nil ||
		!isLowerHexDigest(generationID) {
		return diskGCGenerationIdentity{}, validatedGeneration{}, false
	}
	directory, err := diskOpenDirectoryAt(
		state.generations, generationID,
	)
	if err != nil {
		return diskGCGenerationIdentity{}, validatedGeneration{}, false
	}
	defer directory.Close()
	directoryIdentity, directoryValid := diskGCDirectoryIdentity(directory)
	aggregate, aggregateIdentity, aggregateValid :=
		diskGCReadSecureFileAt(
			directory, "aggregate.json",
			canonicalAggregateByteLimit,
		)
	manifestRaw, manifestIdentity, manifestValid :=
		diskGCReadSecureFileAt(
			directory, "manifest.json", diskStateSchemaLimit,
		)
	statusRaw, statusIdentity, statusValid :=
		diskGCReadSecureFileAt(
			directory, "status.json", diskStateSchemaLimit,
		)
	if !directoryValid || !aggregateValid ||
		!manifestValid || !statusValid ||
		!diskGenerationDirectoryMatches(
			directory, aggregate, manifestRaw, statusRaw,
		) {
		return diskGCGenerationIdentity{}, validatedGeneration{}, false
	}
	manifest, err := diskDecodeManifest(manifestRaw)
	if err != nil ||
		!diskValidManifestHeader(manifest, generationID) {
		return diskGCGenerationIdentity{}, validatedGeneration{}, false
	}
	generation, err := diskLoadGeneration(
		ctx, state.generations, state.objects, generationID,
	)
	if err != nil ||
		!diskGenerationDirectoryMatches(
			directory, aggregate, manifestRaw, statusRaw,
		) {
		return diskGCGenerationIdentity{}, validatedGeneration{}, false
	}
	afterDirectory, directoryStillValid :=
		diskGCDirectoryIdentity(directory)
	if !directoryStillValid || afterDirectory != directoryIdentity ||
		!diskGCFileIdentityStillMatches(
			directory, "aggregate.json", aggregateIdentity,
		) ||
		!diskGCFileIdentityStillMatches(
			directory, "manifest.json", manifestIdentity,
		) ||
		!diskGCFileIdentityStillMatches(
			directory, "status.json", statusIdentity,
		) {
		return diskGCGenerationIdentity{}, validatedGeneration{}, false
	}
	return diskGCGenerationIdentity{
		generationID: generationID,
		parentID:     manifest.Parent,
		directory:    directoryIdentity,
		aggregate:    aggregateIdentity,
		manifest:     manifestIdentity,
		status:       statusIdentity,
	}, generation, true
}

func diskGCEntryNames(directory *os.File) ([]string, bool) {
	if directory == nil {
		return nil, false
	}
	duplicateFD, err := unix.Dup(int(directory.Fd()))
	if err != nil {
		return nil, false
	}
	if _, err := unix.Seek(
		duplicateFD, 0, io.SeekStart,
	); err != nil {
		_ = unix.Close(duplicateFD)
		return nil, false
	}
	copyDirectory := os.NewFile(uintptr(duplicateFD), directory.Name())
	defer copyDirectory.Close()
	entries, err := copyDirectory.ReadDir(-1)
	if err != nil {
		return nil, false
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == "" || strings.Contains(entry.Name(), "/") {
			return nil, false
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, true
}

func diskGCLstatAt(
	directory *os.File,
	name string,
) (unix.Stat_t, bool) {
	var stat unix.Stat_t
	if directory == nil || name == "" || strings.Contains(name, "/") ||
		unix.Fstatat(
			int(directory.Fd()), name, &stat,
			unix.AT_SYMLINK_NOFOLLOW,
		) != nil {
		return unix.Stat_t{}, false
	}
	return stat, true
}

func diskGCDirectoryIdentity(
	directory *os.File,
) (diskGCFileIdentity, bool) {
	var stat unix.Stat_t
	if directory == nil ||
		unix.Fstat(int(directory.Fd()), &stat) != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Mode&07777 != 0700 {
		return diskGCFileIdentity{}, false
	}
	return diskGCFileIdentityFromStat(stat, ""), true
}

func diskGCReadSecureFileAt(
	directory *os.File,
	name string,
	limit int,
) ([]byte, diskGCFileIdentity, bool) {
	if directory == nil || name == "" || strings.Contains(name, "/") ||
		limit < 1 {
		return nil, diskGCFileIdentity{}, false
	}
	fd, err := unix.Openat(
		int(directory.Fd()), name,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, diskGCFileIdentity{}, false
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil ||
		before.Mode&unix.S_IFMT != unix.S_IFREG ||
		before.Mode&07777 != 0600 ||
		before.Nlink != 1 ||
		before.Size < 1 ||
		before.Size > int64(limit) {
		return nil, diskGCFileIdentity{}, false
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(contents) < 1 || len(contents) > limit ||
		int64(len(contents)) != before.Size {
		return nil, diskGCFileIdentity{}, false
	}
	var after unix.Stat_t
	if unix.Fstat(fd, &after) != nil ||
		!legacySameFileStat(before, after) {
		return nil, diskGCFileIdentity{}, false
	}
	identity := diskGCFileIdentityFromStat(
		after, diskSHA256(contents),
	)
	if !diskGCFileIdentityStillMatches(
		directory, name, identity,
	) {
		return nil, diskGCFileIdentity{}, false
	}
	return contents, identity, true
}

func diskGCFileIdentityFromStat(
	stat unix.Stat_t,
	digest string,
) diskGCFileIdentity {
	return diskGCFileIdentity{
		device: uint64(stat.Dev),
		inode:  stat.Ino,
		mode:   stat.Mode,
		nlink:  stat.Nlink,
		size:   stat.Size,
		digest: digest,
	}
}

func diskGCFileIdentityStillMatches(
	directory *os.File,
	name string,
	expected diskGCFileIdentity,
) bool {
	stat, valid := diskGCLstatAt(directory, name)
	if !valid {
		return false
	}
	actual := diskGCFileIdentityFromStat(stat, expected.digest)
	return actual == expected
}

func diskGCObjectDigest(name string) (string, bool) {
	const suffix = ".json"
	if !strings.HasSuffix(name, suffix) {
		return "", false
	}
	digest := strings.TrimSuffix(name, suffix)
	return digest, len(digest) == 64 && isLowerHexDigest(digest)
}

func diskQuarantineAndRemoveGCGeneration(
	ctx context.Context,
	state *diskOpenedState,
	identity diskGCGenerationIdentity,
	filesystem diskGenerationFilesystem,
	remover diskGenerationRemoveFilesystem,
) bool {
	if ctx == nil || ctx.Err() != nil || state == nil ||
		state.generations == nil || filesystem == nil || remover == nil {
		return false
	}
	quarantineName := diskPrivateName(
		diskGCGenerationQuarantinePrefix +
			identity.generationID + "-",
	)
	if filesystem.RenameNoReplace(
		state.generations, identity.generationID,
		state.generations, quarantineName,
	) != nil {
		return false
	}
	snapshot, directory, valid := diskCaptureGCGenerationAt(
		state.generations, quarantineName, identity,
	)
	if !valid {
		if !diskRepairSwappedGCGeneration(
			ctx, state, quarantineName, identity, filesystem,
		) {
			_ = diskRestoreGCQuarantineName(
				state.generations,
				quarantineName,
				identity.generationID,
				filesystem,
			)
			return false
		}
		snapshot, directory, valid = diskCaptureGCGenerationAt(
			state.generations, quarantineName, identity,
		)
		if !valid {
			return false
		}
	}
	if filesystem.Sync(state.generations) != nil {
		_ = directory.Close()
		return false
	}
	return diskRemoveGCQuarantinedGeneration(
		state.generations,
		quarantineName,
		snapshot,
		directory,
		filesystem,
		remover,
	)
}

func diskRemoveGCQuarantinedGeneration(
	generations *os.File,
	quarantineName string,
	_ diskGCGenerationSnapshot,
	directory *os.File,
	filesystem diskGenerationFilesystem,
	remover diskGenerationRemoveFilesystem,
) bool {
	if generations == nil || directory == nil || filesystem == nil ||
		remover == nil ||
		!diskGCValidQuarantineName(
			quarantineName, diskGCGenerationQuarantinePrefix,
		) {
		if directory != nil {
			_ = directory.Close()
		}
		return false
	}
	defer directory.Close()
	for _, name := range []string{
		"aggregate.json", "manifest.json", "status.json",
	} {
		if remover.RemoveAt(directory, name, 0) != nil {
			_ = filesystem.Sync(directory)
			return false
		}
	}
	if filesystem.Sync(directory) != nil {
		return false
	}
	if remover.RemoveAt(
		generations,
		quarantineName,
		unix.AT_REMOVEDIR,
	) != nil {
		return false
	}
	return filesystem.Sync(generations) == nil
}

func diskRestoreGCQuarantineName(
	directory *os.File,
	quarantineName string,
	publicName string,
	filesystem diskGenerationFilesystem,
) bool {
	if directory == nil || filesystem == nil ||
		quarantineName == "" || publicName == "" ||
		strings.Contains(quarantineName, "/") ||
		strings.Contains(publicName, "/") {
		return false
	}
	if unix.Renameat2(
		int(directory.Fd()), quarantineName,
		int(directory.Fd()), publicName,
		unix.RENAME_NOREPLACE,
	) != nil {
		return false
	}
	return filesystem.Sync(directory) == nil
}

func diskCaptureGCGenerationAt(
	generations *os.File,
	name string,
	identity diskGCGenerationIdentity,
) (diskGCGenerationSnapshot, *os.File, bool) {
	directory, err := diskOpenDirectoryAt(
		generations, name,
	)
	if err != nil {
		return diskGCGenerationSnapshot{}, nil, false
	}
	directoryIdentity, valid := diskGCDirectoryIdentity(directory)
	if !valid || directoryIdentity != identity.directory {
		_ = directory.Close()
		return diskGCGenerationSnapshot{}, nil, false
	}
	aggregate, aggregateIdentity, aggregateValid :=
		diskGCReadSecureFileAt(
			directory, "aggregate.json",
			canonicalAggregateByteLimit,
		)
	manifest, manifestIdentity, manifestValid :=
		diskGCReadSecureFileAt(
			directory, "manifest.json", diskStateSchemaLimit,
		)
	status, statusIdentity, statusValid :=
		diskGCReadSecureFileAt(
			directory, "status.json", diskStateSchemaLimit,
		)
	if !aggregateValid || !manifestValid || !statusValid ||
		aggregateIdentity != identity.aggregate ||
		manifestIdentity != identity.manifest ||
		statusIdentity != identity.status ||
		!diskGenerationDirectoryMatches(
			directory, aggregate, manifest, status,
		) {
		_ = directory.Close()
		return diskGCGenerationSnapshot{}, nil, false
	}
	return diskGCGenerationSnapshot{
		aggregate: aggregate,
		manifest:  manifest,
		status:    status,
	}, directory, true
}

func diskDescribeGCGenerationAt(
	generations *os.File,
	name string,
) (
	diskGCGenerationIdentity,
	diskGCGenerationSnapshot,
	bool,
) {
	directory, err := diskOpenDirectoryAt(generations, name)
	if err != nil {
		return diskGCGenerationIdentity{},
			diskGCGenerationSnapshot{}, false
	}
	defer directory.Close()
	directoryIdentity, directoryValid :=
		diskGCDirectoryIdentity(directory)
	aggregate, aggregateIdentity, aggregateValid :=
		diskGCReadSecureFileAt(
			directory, "aggregate.json",
			canonicalAggregateByteLimit,
		)
	manifestRaw, manifestIdentity, manifestValid :=
		diskGCReadSecureFileAt(
			directory, "manifest.json", diskStateSchemaLimit,
		)
	statusRaw, statusIdentity, statusValid :=
		diskGCReadSecureFileAt(
			directory, "status.json", diskStateSchemaLimit,
		)
	if !directoryValid || !aggregateValid ||
		!manifestValid || !statusValid ||
		!diskGenerationDirectoryMatches(
			directory, aggregate, manifestRaw, statusRaw,
		) {
		return diskGCGenerationIdentity{},
			diskGCGenerationSnapshot{}, false
	}
	manifest, manifestErr := diskDecodeManifest(manifestRaw)
	status, statusErr := diskDecodeStatus(statusRaw)
	canonical, aggregateCount, canonicalErr :=
		canonicalizeStoredSource(aggregate)
	if manifestErr != nil || statusErr != nil ||
		!diskValidManifestHeader(manifest, manifest.Generation) ||
		status.Schema != 1 ||
		status.Generation != manifest.Generation ||
		diskSHA256(statusRaw) != manifest.StatusSHA256 ||
		diskSHA256(aggregate) != manifest.Aggregate.SHA256 ||
		len(aggregate) != manifest.Aggregate.Bytes ||
		canonicalErr != nil || !bytes.Equal(canonical, aggregate) ||
		aggregateCount != manifest.Aggregate.Outbounds {
		return diskGCGenerationIdentity{},
			diskGCGenerationSnapshot{}, false
	}
	return diskGCGenerationIdentity{
			generationID: manifest.Generation,
			parentID:     manifest.Parent,
			directory:    directoryIdentity,
			aggregate:    aggregateIdentity,
			manifest:     manifestIdentity,
			status:       statusIdentity,
		}, diskGCGenerationSnapshot{
			aggregate: aggregate,
			manifest:  manifestRaw,
			status:    statusRaw,
		}, true
}

func diskRepairSwappedGCGeneration(
	ctx context.Context,
	state *diskOpenedState,
	quarantineName string,
	expected diskGCGenerationIdentity,
	filesystem diskGenerationFilesystem,
) bool {
	if ctx == nil || ctx.Err() != nil || state == nil ||
		state.generations == nil || filesystem == nil {
		return false
	}
	moved, _, valid := diskDescribeGCGenerationAt(
		state.generations, quarantineName,
	)
	if !valid || moved.generationID == expected.generationID {
		return false
	}
	_, target, targetValid := diskCaptureGCGenerationAt(
		state.generations, moved.generationID, expected,
	)
	if !targetValid {
		return false
	}
	_ = target.Close()
	if !diskGCExchangeNames(
		state.generations, quarantineName, moved.generationID,
	) || filesystem.Sync(state.generations) != nil {
		return false
	}
	restored, _, restoredValid := diskInspectGCGeneration(
		ctx, state, moved.generationID,
	)
	_, quarantined, quarantinedValid :=
		diskCaptureGCGenerationAt(
			state.generations, quarantineName, expected,
		)
	if quarantined != nil {
		_ = quarantined.Close()
	}
	return restoredValid && restored == moved &&
		quarantinedValid
}

func diskGCRetainedStillMatches(
	ctx context.Context,
	state *diskOpenedState,
	currentGenerationID string,
	retained map[string]diskGCGenerationIdentity,
) bool {
	current, err := diskLoadCurrentFromOpened(ctx, state)
	if err != nil || current.Kind != currentPresent ||
		current.Generation.GenerationID != currentGenerationID {
		return false
	}
	for generationID, expected := range retained {
		actual, _, valid := diskInspectGCGeneration(
			ctx, state, generationID,
		)
		if !valid || actual != expected {
			return false
		}
	}
	return true
}

func diskGCObjectStillMatches(
	objects *os.File,
	identity diskGCObjectIdentity,
) bool {
	contents, actual, valid := diskGCReadSecureFileAt(
		objects, identity.name, canonicalAggregateByteLimit,
	)
	if !valid || actual != identity.file ||
		actual.digest != strings.TrimSuffix(identity.name, ".json") {
		return false
	}
	canonical, _, err := canonicalizeStoredSource(contents)
	return err == nil && bytes.Equal(canonical, contents)
}

func diskQuarantineAndRemoveGCObject(
	objects *os.File,
	identity diskGCObjectIdentity,
	filesystem diskGenerationFilesystem,
	remover diskGenerationRemoveFilesystem,
) bool {
	if objects == nil || filesystem == nil || remover == nil {
		return false
	}
	digest, valid := diskGCObjectDigest(identity.name)
	if !valid {
		return false
	}
	quarantineName := diskPrivateName(
		diskGCObjectQuarantinePrefix + digest + "-",
	)
	if filesystem.RenameNoReplace(
		objects, identity.name,
		objects, quarantineName,
	) != nil {
		return false
	}
	if !diskGCObjectAtMatches(
		objects, quarantineName, identity.file, digest,
	) {
		if !diskRepairSwappedGCObject(
			objects, quarantineName, identity, filesystem,
		) {
			_ = diskRestoreGCQuarantineName(
				objects, quarantineName, identity.name, filesystem,
			)
			return false
		}
	}
	if !diskGCObjectAtMatches(
		objects, quarantineName, identity.file, digest,
	) || filesystem.Sync(objects) != nil {
		return false
	}
	if remover.RemoveAt(objects, quarantineName, 0) != nil {
		return false
	}
	return filesystem.Sync(objects) == nil
}

func diskGCObjectAtMatches(
	objects *os.File,
	name string,
	expected diskGCFileIdentity,
	expectedDigest string,
) bool {
	contents, actual, valid := diskGCReadSecureFileAt(
		objects, name, canonicalAggregateByteLimit,
	)
	if !valid || actual != expected || actual.digest != expectedDigest {
		return false
	}
	canonical, _, err := canonicalizeStoredSource(contents)
	return err == nil && bytes.Equal(canonical, contents)
}

func diskRepairSwappedGCObject(
	objects *os.File,
	quarantineName string,
	expected diskGCObjectIdentity,
	filesystem diskGenerationFilesystem,
) bool {
	contents, moved, valid := diskGCReadSecureFileAt(
		objects, quarantineName, canonicalAggregateByteLimit,
	)
	if !valid {
		return false
	}
	canonical, _, err := canonicalizeStoredSource(contents)
	if err != nil || !bytes.Equal(canonical, contents) {
		return false
	}
	actualName := moved.digest + ".json"
	if actualName == expected.name ||
		!diskGCObjectAtMatches(
			objects,
			actualName,
			expected.file,
			strings.TrimSuffix(expected.name, ".json"),
		) {
		return false
	}
	if !diskGCExchangeNames(objects, quarantineName, actualName) ||
		filesystem.Sync(objects) != nil {
		return false
	}
	return diskGCObjectAtMatches(
		objects, actualName, moved, moved.digest,
	) && diskGCObjectAtMatches(
		objects,
		quarantineName,
		expected.file,
		strings.TrimSuffix(expected.name, ".json"),
	)
}

func diskGCExchangeNames(
	directory *os.File,
	left string,
	right string,
) bool {
	if directory == nil || left == "" || right == "" ||
		left == right || strings.Contains(left, "/") ||
		strings.Contains(right, "/") {
		return false
	}
	return unix.Renameat2(
		int(directory.Fd()), left,
		int(directory.Fd()), right,
		unix.RENAME_EXCHANGE,
	) == nil
}

func diskGCValidQuarantineName(
	name string,
	prefix string,
) bool {
	_, valid := diskGCQuarantineDigest(name, prefix)
	return valid
}

func diskGCQuarantineDigest(
	name string,
	prefix string,
) (string, bool) {
	if prefix == "" || !strings.HasPrefix(name, prefix) ||
		strings.Contains(name, "/") {
		return "", false
	}
	rest := strings.TrimPrefix(name, prefix)
	if len(rest) < 64+1+1 || rest[64] != '-' {
		return "", false
	}
	digest := rest[:64]
	suffix := rest[65:]
	separator := strings.IndexByte(suffix, '-')
	if !isLowerHexDigest(digest) || separator < 1 ||
		separator == len(suffix)-1 ||
		len(suffix[separator+1:]) != 16 {
		return "", false
	}
	for _, char := range []byte(suffix[:separator]) {
		if char < '0' || char > '9' {
			return "", false
		}
	}
	for _, char := range []byte(suffix[separator+1:]) {
		if !((char >= '0' && char <= '9') ||
			(char >= 'a' && char <= 'f')) {
			return "", false
		}
	}
	return digest, true
}

func diskRecoverGCQuarantines(
	ctx context.Context,
	state *diskOpenedState,
	currentGenerationID string,
	filesystem diskGenerationFilesystem,
	remover diskGenerationRemoveFilesystem,
) bool {
	if ctx == nil || ctx.Err() != nil || state == nil ||
		state.generations == nil || state.objects == nil ||
		!isLowerHexDigest(currentGenerationID) ||
		filesystem == nil || remover == nil {
		return false
	}
	objectNames, valid := diskGCEntryNames(state.objects)
	if !valid {
		return false
	}
	for _, name := range objectNames {
		if _, isQuarantine := diskGCQuarantineDigest(
			name, diskGCObjectQuarantinePrefix,
		); !isQuarantine {
			continue
		}
		if ctx.Err() != nil ||
			!diskRecoverGCObjectQuarantine(
				state.objects, name, filesystem,
			) {
			return false
		}
	}
	generationNames, valid := diskGCEntryNames(state.generations)
	if !valid {
		return false
	}
	for _, name := range generationNames {
		if _, isQuarantine := diskGCQuarantineDigest(
			name, diskGCGenerationQuarantinePrefix,
		); !isQuarantine {
			continue
		}
		if ctx.Err() != nil ||
			!diskRecoverGCGenerationQuarantine(
				state.generations,
				name,
				currentGenerationID,
				filesystem,
				remover,
			) {
			return false
		}
	}
	return true
}

func diskRecoverGCObjectQuarantine(
	objects *os.File,
	quarantineName string,
	filesystem diskGenerationFilesystem,
) bool {
	intendedDigest, valid := diskGCQuarantineDigest(
		quarantineName, diskGCObjectQuarantinePrefix,
	)
	if !valid {
		return false
	}
	contents, moved, valid := diskGCReadSecureFileAt(
		objects, quarantineName, canonicalAggregateByteLimit,
	)
	if !valid {
		return false
	}
	canonical, _, err := canonicalizeStoredSource(contents)
	if err != nil || !bytes.Equal(canonical, contents) {
		return false
	}
	if moved.digest != intendedDigest {
		actualName := moved.digest + ".json"
		targetContents, target, targetValid :=
			diskGCReadSecureFileAt(
				objects, actualName, canonicalAggregateByteLimit,
			)
		if !targetValid || target.digest != intendedDigest {
			return false
		}
		targetCanonical, _, targetErr :=
			canonicalizeStoredSource(targetContents)
		if targetErr != nil ||
			!bytes.Equal(targetCanonical, targetContents) ||
			!diskGCExchangeNames(
				objects, quarantineName, actualName,
			) || filesystem.Sync(objects) != nil {
			return false
		}
		if !diskGCObjectAtMatches(
			objects, actualName, moved, moved.digest,
		) || !diskGCObjectAtMatches(
			objects,
			quarantineName,
			target,
			intendedDigest,
		) {
			return false
		}
	}
	publicName := intendedDigest + ".json"
	if !diskGCNameAbsent(objects, publicName) {
		publicContents, public, publicValid :=
			diskGCReadSecureFileAt(
				objects, publicName, canonicalAggregateByteLimit,
			)
		if !publicValid || public.digest != intendedDigest {
			return false
		}
		publicCanonical, _, publicErr :=
			canonicalizeStoredSource(publicContents)
		if publicErr != nil ||
			!bytes.Equal(publicCanonical, publicContents) {
			return false
		}
		// A later commit may have republished the same content. The private
		// copy is then redundant and can be removed safely.
		if unix.Unlinkat(
			int(objects.Fd()), quarantineName, 0,
		) != nil {
			return false
		}
		return filesystem.Sync(objects) == nil
	}
	return diskRestoreGCQuarantineName(
		objects, quarantineName, publicName, filesystem,
	)
}

func diskRecoverGCGenerationQuarantine(
	generations *os.File,
	quarantineName string,
	currentGenerationID string,
	filesystem diskGenerationFilesystem,
	remover diskGenerationRemoveFilesystem,
) bool {
	intendedGenerationID, valid := diskGCQuarantineDigest(
		quarantineName, diskGCGenerationQuarantinePrefix,
	)
	if !valid {
		return false
	}
	moved, _, complete := diskDescribeGCGenerationAt(
		generations, quarantineName,
	)
	if !complete {
		return diskRemovePartialGCGenerationQuarantine(
			generations,
			quarantineName,
			filesystem,
			remover,
		)
	}
	if moved.generationID != intendedGenerationID {
		target, _, targetValid := diskDescribeGCGenerationAt(
			generations, moved.generationID,
		)
		if !targetValid ||
			target.generationID != intendedGenerationID ||
			!diskGCExchangeNames(
				generations,
				quarantineName,
				moved.generationID,
			) || filesystem.Sync(generations) != nil {
			return false
		}
		restored, _, restoredValid :=
			diskDescribeGCGenerationAt(
				generations, moved.generationID,
			)
		quarantined, _, quarantinedValid :=
			diskDescribeGCGenerationAt(
				generations, quarantineName,
			)
		if !restoredValid || restored != moved ||
			!quarantinedValid ||
			quarantined.generationID != intendedGenerationID {
			return false
		}
	}
	if intendedGenerationID == currentGenerationID {
		if !diskGCNameAbsent(
			generations, intendedGenerationID,
		) {
			return false
		}
		return diskRestoreGCQuarantineName(
			generations,
			quarantineName,
			intendedGenerationID,
			filesystem,
		)
	}
	if !diskGCNameAbsent(generations, intendedGenerationID) {
		return false
	}
	return diskRestoreGCQuarantineName(
		generations,
		quarantineName,
		intendedGenerationID,
		filesystem,
	)
}

func diskRemovePartialGCGenerationQuarantine(
	generations *os.File,
	quarantineName string,
	filesystem diskGenerationFilesystem,
	remover diskGenerationRemoveFilesystem,
) bool {
	directory, err := diskOpenDirectoryAt(
		generations, quarantineName,
	)
	if err != nil {
		return false
	}
	defer directory.Close()
	names, valid := diskGCEntryNames(directory)
	if !valid || len(names) >= 3 {
		return false
	}
	allowed := map[string]bool{
		"aggregate.json": true,
		"manifest.json":  true,
		"status.json":    true,
	}
	for _, name := range names {
		stat, statValid := diskGCLstatAt(directory, name)
		if !allowed[name] || !statValid ||
			stat.Mode&unix.S_IFMT != unix.S_IFREG ||
			stat.Mode&07777 != 0600 || stat.Nlink != 1 {
			return false
		}
	}
	for _, name := range names {
		if remover.RemoveAt(directory, name, 0) != nil {
			_ = filesystem.Sync(directory)
			return false
		}
	}
	if filesystem.Sync(directory) != nil ||
		remover.RemoveAt(
			generations, quarantineName, unix.AT_REMOVEDIR,
		) != nil {
		return false
	}
	return filesystem.Sync(generations) == nil
}

func diskGCNameAbsent(
	directory *os.File,
	name string,
) bool {
	if directory == nil || name == "" || strings.Contains(name, "/") {
		return false
	}
	var stat unix.Stat_t
	err := unix.Fstatat(
		int(directory.Fd()), name, &stat,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	return errors.Is(err, unix.ENOENT)
}
