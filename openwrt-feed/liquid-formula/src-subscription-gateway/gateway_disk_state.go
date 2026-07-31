package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	diskStateSchemaLimit = 64 * 1024
	diskCurrentBytes     = 65
)

var errDiskStateInvalid = errors.New("subscription state invalid")

type diskAggregateMetadata struct {
	SHA256    string `json:"sha256"`
	Bytes     int    `json:"bytes"`
	Outbounds int    `json:"outbounds"`
}

type diskManifestSource struct {
	Index        int    `json:"index"`
	URLSHA256    string `json:"url_sha256"`
	ObjectSHA256 string `json:"object_sha256"`
	Bytes        int    `json:"bytes"`
	Outbounds    int    `json:"outbounds"`
}

type diskManifest struct {
	Schema                  int                   `json:"schema"`
	Generation              string                `json:"generation"`
	Parent                  string                `json:"parent"`
	ConfigDigest            string                `json:"config_digest"`
	Aggregate               diskAggregateMetadata `json:"aggregate"`
	StatusSHA256            string                `json:"status_sha256"`
	Sources                 []diskManifestSource  `json:"sources"`
	LegacyConsumedURLSHA256 string                `json:"legacy_consumed_url_sha256"`
}

type diskStatusWarning struct {
	Code      string `json:"code"`
	NodeIndex int    `json:"node_index"`
	Type      string `json:"type"`
	Field     string `json:"field"`
}

type diskStatusSource struct {
	Index     int                 `json:"index"`
	Result    string              `json:"result"`
	FetchCode string              `json:"fetch_code"`
	Format    string              `json:"format"`
	Accepted  int                 `json:"accepted"`
	Skipped   int                 `json:"skipped"`
	Warnings  []diskStatusWarning `json:"warnings"`
}

type diskStatus struct {
	Schema          int                `json:"schema"`
	Generation      string             `json:"generation"`
	State           string             `json:"state"`
	FreshCount      int                `json:"fresh_count"`
	FallbackIndices []int              `json:"fallback_indices"`
	Sources         []diskStatusSource `json:"sources"`
}

type diskGenerationFilesystem interface {
	Sync(*os.File) error
	RenameNoReplace(
		*os.File, string, *os.File, string,
	) error
	RenameReplace(
		*os.File, string, *os.File, string,
	) error
}

type diskGenerationRemoveFilesystem interface {
	RemoveAt(*os.File, string, int) error
}

type nativeDiskGenerationFilesystem struct{}

func (nativeDiskGenerationFilesystem) Sync(file *os.File) error {
	return file.Sync()
}

func (nativeDiskGenerationFilesystem) RenameNoReplace(
	oldDirectory *os.File,
	oldName string,
	newDirectory *os.File,
	newName string,
) error {
	return unix.Renameat2(
		int(oldDirectory.Fd()), oldName,
		int(newDirectory.Fd()), newName,
		unix.RENAME_NOREPLACE,
	)
}

func (nativeDiskGenerationFilesystem) RenameReplace(
	oldDirectory *os.File,
	oldName string,
	newDirectory *os.File,
	newName string,
) error {
	return unix.Renameat(
		int(oldDirectory.Fd()), oldName,
		int(newDirectory.Fd()), newName,
	)
}

func (nativeDiskGenerationFilesystem) RemoveAt(
	directory *os.File,
	name string,
	flags int,
) error {
	if flags != 0 && flags != unix.AT_REMOVEDIR {
		return unix.EINVAL
	}
	return unix.Unlinkat(int(directory.Fd()), name, flags)
}

type diskGenerationStore struct {
	root string

	generationRandom io.Reader
	filesystem       diskGenerationFilesystem
	faultHook        func(string) error
	legacy           legacySourceProvider
}

func newDiskGenerationStore(root string) generationStore {
	return newDiskGenerationStoreWithDependencies(
		root, diskGenerationStoreDependencies{},
	)
}

func (store *diskGenerationStore) ObserveCurrent(
	ctx context.Context,
) (currentObservation, error) {
	selection, err := store.loadCurrent(ctx)
	if err != nil {
		return currentObservation{Kind: currentInvalid}, err
	}
	if selection.Kind == currentAbsent {
		return currentObservation{Kind: currentAbsent}, nil
	}
	return currentObservation{
		Kind:         currentPresent,
		GenerationID: selection.Generation.GenerationID,
		Validated:    true,
	}, nil
}

func (store *diskGenerationStore) LoadCurrent(
	ctx context.Context,
) (currentSelection, error) {
	return store.loadCurrent(ctx)
}

type diskOpenedState struct {
	root        *os.File
	generations *os.File
	objects     *os.File
}

func (state *diskOpenedState) close() {
	if state == nil {
		return
	}
	if state.objects != nil {
		_ = state.objects.Close()
	}
	if state.generations != nil {
		_ = state.generations.Close()
	}
	if state.root != nil {
		_ = state.root.Close()
	}
}

func (store *diskGenerationStore) loadCurrent(
	ctx context.Context,
) (currentSelection, error) {
	if ctx == nil || ctx.Err() != nil || store == nil ||
		store.root == "" {
		return currentSelection{Kind: currentInvalid}, errDiskStateInvalid
	}
	state, absent, err := diskOpenExistingState(store.root)
	if err != nil {
		return currentSelection{Kind: currentInvalid}, err
	}
	if absent {
		return currentSelection{Kind: currentAbsent}, nil
	}
	defer state.close()
	return diskLoadCurrentFromOpened(ctx, state)
}

func diskOpenExistingState(
	root string,
) (*diskOpenedState, bool, error) {
	rootFile, err := diskOpenDirectoryPath(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, errDiskStateInvalid
	}
	state := &diskOpenedState{root: rootFile}
	generations, err := diskOpenDirectoryAt(rootFile, "generations")
	if err != nil {
		state.close()
		return nil, false, errDiskStateInvalid
	}
	state.generations = generations
	objects, err := diskOpenDirectoryAt(rootFile, "objects")
	if err != nil {
		state.close()
		return nil, false, errDiskStateInvalid
	}
	state.objects = objects
	return state, false, nil
}

func diskOpenDirectoryPath(path string) (*os.File, error) {
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if !diskValidDirectory(file) {
		_ = file.Close()
		return nil, errDiskStateInvalid
	}
	return file, nil
}

func diskOpenDirectoryAt(
	parent *os.File,
	name string,
) (*os.File, error) {
	file, err := diskOpenDirectoryAtUnchecked(parent, name)
	if err != nil {
		return nil, err
	}
	if !diskValidDirectory(file) {
		_ = file.Close()
		return nil, errDiskStateInvalid
	}
	return file, nil
}

func diskOpenDirectoryAtUnchecked(
	parent *os.File,
	name string,
) (*os.File, error) {
	fd, err := unix.Openat(
		int(parent.Fd()), name,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, err
	}
	path := parent.Name() + string(os.PathSeparator) + name
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = file.Close()
		return nil, errDiskStateInvalid
	}
	return file, nil
}

func diskValidDirectory(file *os.File) bool {
	var stat unix.Stat_t
	if file == nil || unix.Fstat(int(file.Fd()), &stat) != nil {
		return false
	}
	return stat.Mode&unix.S_IFMT == unix.S_IFDIR &&
		stat.Mode&07777 == 0700
}

func diskReadSecureFileAt(
	directory *os.File,
	name string,
	limit int,
) ([]byte, bool, error) {
	fd, err := unix.Openat(
		int(directory.Fd()), name,
		unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if errors.Is(err, unix.ENOENT) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, errDiskStateInvalid
	}
	path := directory.Name() + string(os.PathSeparator) + name
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&07777 != 0600 ||
		stat.Nlink != 1 ||
		stat.Size < 0 ||
		stat.Size > int64(limit) {
		return nil, false, errDiskStateInvalid
	}
	contents, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(contents) > limit ||
		int64(len(contents)) != stat.Size {
		return nil, false, errDiskStateInvalid
	}
	return contents, false, nil
}

func diskLoadGeneration(
	ctx context.Context,
	generations *os.File,
	objects *os.File,
	generationID string,
) (validatedGeneration, error) {
	if ctx == nil || ctx.Err() != nil ||
		!isLowerHexDigest(generationID) {
		return validatedGeneration{}, errDiskStateInvalid
	}
	generationDirectory, err := diskOpenDirectoryAt(
		generations, generationID,
	)
	if err != nil {
		return validatedGeneration{}, errDiskStateInvalid
	}
	defer generationDirectory.Close()
	if !diskGenerationHasExactFiles(generationDirectory) {
		return validatedGeneration{}, errDiskStateInvalid
	}
	aggregate, missing, err := diskReadSecureFileAt(
		generationDirectory, "aggregate.json",
		canonicalAggregateByteLimit,
	)
	if err != nil || missing {
		return validatedGeneration{}, errDiskStateInvalid
	}
	manifestRaw, missing, err := diskReadSecureFileAt(
		generationDirectory, "manifest.json", diskStateSchemaLimit,
	)
	if err != nil || missing {
		return validatedGeneration{}, errDiskStateInvalid
	}
	statusRaw, missing, err := diskReadSecureFileAt(
		generationDirectory, "status.json", diskStateSchemaLimit,
	)
	if err != nil || missing {
		return validatedGeneration{}, errDiskStateInvalid
	}
	manifest, err := diskDecodeManifest(manifestRaw)
	if err != nil {
		return validatedGeneration{}, errDiskStateInvalid
	}
	status, err := diskDecodeStatus(statusRaw)
	if err != nil {
		return validatedGeneration{}, errDiskStateInvalid
	}
	if ctx.Err() != nil ||
		!diskValidManifestHeader(manifest, generationID) ||
		status.Schema != 1 ||
		status.Generation != generationID ||
		diskSHA256(statusRaw) != manifest.StatusSHA256 {
		return validatedGeneration{}, errDiskStateInvalid
	}

	if diskSHA256(aggregate) != manifest.Aggregate.SHA256 ||
		len(aggregate) != manifest.Aggregate.Bytes ||
		len(aggregate) == 0 {
		return validatedGeneration{}, errDiskStateInvalid
	}
	if len(manifest.Sources) < 1 ||
		len(manifest.Sources) > 8 ||
		len(status.Sources) != len(manifest.Sources) {
		return validatedGeneration{}, errDiskStateInvalid
	}

	objectBackings := make(map[string][]byte)
	objectCounts := make(map[string]int)
	sources := make([]generationSource, len(manifest.Sources))
	type duplicateMetadata struct {
		objectDigest string
		bytes        int
		outbounds    int
		result       string
		fetchCode    string
		info         NormalizeInfo
	}
	duplicates := make(map[string]duplicateMetadata)
	fallbackIndices := make([]int, 0)
	freshCount := 0
	for offset := range manifest.Sources {
		if ctx.Err() != nil {
			return validatedGeneration{}, errDiskStateInvalid
		}
		manifestSource := manifest.Sources[offset]
		statusSource := status.Sources[offset]
		index := offset + 1
		if manifestSource.Index != index ||
			statusSource.Index != index ||
			!isLowerHexDigest(manifestSource.URLSHA256) ||
			!isLowerHexDigest(manifestSource.ObjectSHA256) ||
			manifestSource.Bytes < 1 ||
			manifestSource.Outbounds < 1 ||
			manifestSource.Outbounds > MaxNormalizedNodes {
			return validatedGeneration{}, errDiskStateInvalid
		}
		object := objectBackings[manifestSource.ObjectSHA256]
		objectCount, loaded := objectCounts[manifestSource.ObjectSHA256]
		if !loaded {
			objectName := manifestSource.ObjectSHA256 + ".json"
			object, missing, err = diskReadSecureFileAt(
				objects, objectName, canonicalAggregateByteLimit,
			)
			if err != nil || missing ||
				diskSHA256(object) != manifestSource.ObjectSHA256 {
				return validatedGeneration{}, errDiskStateInvalid
			}
			canonicalObject, count, canonicalErr :=
				canonicalizeStoredSource(object)
			if canonicalErr != nil ||
				!bytes.Equal(canonicalObject, object) {
				return validatedGeneration{}, errDiskStateInvalid
			}
			objectCount = count
			objectBackings[manifestSource.ObjectSHA256] = object
			objectCounts[manifestSource.ObjectSHA256] = objectCount
		}
		if len(object) != manifestSource.Bytes ||
			objectCount != manifestSource.Outbounds {
			return validatedGeneration{}, errDiskStateInvalid
		}
		info, err := diskValidateStatusSource(
			statusSource, objectCount,
		)
		if err != nil {
			return validatedGeneration{}, errDiskStateInvalid
		}
		switch statusSource.Result {
		case sourceResultFresh:
			if statusSource.FetchCode != string(fetchCodeOK) {
				return validatedGeneration{}, errDiskStateInvalid
			}
			freshCount++
		case sourceResultFallback:
			if !validFetchFailureCode(
				sourceFetchCode(statusSource.FetchCode),
			) {
				return validatedGeneration{}, errDiskStateInvalid
			}
			fallbackIndices = append(fallbackIndices, index)
		default:
			return validatedGeneration{}, errDiskStateInvalid
		}

		metadata := duplicateMetadata{
			objectDigest: manifestSource.ObjectSHA256,
			bytes:        manifestSource.Bytes,
			outbounds:    manifestSource.Outbounds,
			result:       statusSource.Result,
			fetchCode:    statusSource.FetchCode,
			info:         info,
		}
		if previous, exists := duplicates[manifestSource.URLSHA256]; exists {
			if previous.objectDigest != metadata.objectDigest ||
				previous.bytes != metadata.bytes ||
				previous.outbounds != metadata.outbounds ||
				previous.result != metadata.result ||
				previous.fetchCode != metadata.fetchCode ||
				!equalNormalizeInfo(previous.info, metadata.info) {
				return validatedGeneration{}, errDiskStateInvalid
			}
		} else {
			duplicates[manifestSource.URLSHA256] = metadata
		}
		sources[offset] = generationSource{
			Index:        index,
			URLDigest:    manifestSource.URLSHA256,
			ObjectDigest: manifestSource.ObjectSHA256,
			Normalized:   object,
			Info:         info,
		}
	}
	if !diskStatusAgrees(
		status, freshCount, fallbackIndices,
	) {
		return validatedGeneration{}, errDiskStateInvalid
	}
	recomputed, err := diskMergeLoadedSources(sources)
	if err != nil || !bytes.Equal(recomputed, aggregate) {
		return validatedGeneration{}, errDiskStateInvalid
	}
	_, aggregateOutbounds, err := canonicalizeStoredSource(
		recomputed,
	)
	if err != nil ||
		aggregateOutbounds != manifest.Aggregate.Outbounds {
		return validatedGeneration{}, errDiskStateInvalid
	}
	return validatedGeneration{
		GenerationID: generationID,
		ConfigDigest: manifest.ConfigDigest,
		Aggregate:    aggregate,
		Sources:      sources,
		LegacyConsumedURLDigest: manifest.
			LegacyConsumedURLSHA256,
	}, nil
}

func diskMergeLoadedSources(
	sources []generationSource,
) ([]byte, error) {
	objects := make([][]byte, 0, len(sources))
	objectIndexes := make(map[string]int)
	candidateSources := make(
		[]generationCandidateSource, len(sources),
	)
	for offset, source := range sources {
		objectIndex, exists := objectIndexes[source.ObjectDigest]
		if !exists {
			objects = append(objects, source.Normalized)
			objectIndex = len(objects)
			objectIndexes[source.ObjectDigest] = objectIndex
		}
		candidateSources[offset] = generationCandidateSource{
			Index:       offset + 1,
			ObjectIndex: objectIndex,
		}
	}
	return mergeCanonicalAggregate(generationCandidate{
		Objects: objects,
		Sources: candidateSources,
	})
}

func diskGenerationHasExactFiles(directory *os.File) bool {
	duplicateFD, err := unix.Dup(int(directory.Fd()))
	if err != nil {
		return false
	}
	if _, err := unix.Seek(duplicateFD, 0, io.SeekStart); err != nil {
		_ = unix.Close(duplicateFD)
		return false
	}
	copyDirectory := os.NewFile(uintptr(duplicateFD), directory.Name())
	defer copyDirectory.Close()
	return diskGenerationHasExactFilesFromReadDir(copyDirectory.ReadDir)
}

func diskGenerationHasExactFilesFromReadDir(
	readDir func(int) ([]os.DirEntry, error),
) bool {
	if readDir == nil {
		return false
	}
	// Four entries are enough to prove that the immutable generation does not
	// contain exactly its three schema files. Never enumerate an attacker-sized
	// directory from the status polling path.
	entries, err := readDir(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	if len(entries) != 3 {
		return false
	}
	expected := map[string]bool{
		"aggregate.json": false,
		"manifest.json":  false,
		"status.json":    false,
	}
	for _, entry := range entries {
		if _, exists := expected[entry.Name()]; !exists {
			return false
		}
		expected[entry.Name()] = true
	}
	for _, present := range expected {
		if !present {
			return false
		}
	}
	return true
}

func diskValidManifestHeader(
	manifest diskManifest,
	generationID string,
) bool {
	return manifest.Schema == 1 &&
		manifest.Generation == generationID &&
		(manifest.Parent == "" ||
			isLowerHexDigest(manifest.Parent)) &&
		isLowerHexDigest(manifest.ConfigDigest) &&
		isLowerHexDigest(manifest.Aggregate.SHA256) &&
		manifest.Aggregate.Bytes > 0 &&
		manifest.Aggregate.Bytes <= canonicalAggregateByteLimit &&
		manifest.Aggregate.Outbounds > 0 &&
		manifest.Aggregate.Outbounds <= MaxNormalizedNodes &&
		isLowerHexDigest(manifest.StatusSHA256) &&
		(manifest.LegacyConsumedURLSHA256 == "" ||
			isLowerHexDigest(
				manifest.LegacyConsumedURLSHA256,
			))
}

func diskValidateStatusSource(
	source diskStatusSource,
	objectCount int,
) (NormalizeInfo, error) {
	format := Format(source.Format)
	switch format {
	case FormatSingBoxJSON,
		FormatBase64URI,
		FormatPlainURI,
		FormatClashYAML:
	default:
		return NormalizeInfo{}, errDiskStateInvalid
	}
	if source.Accepted != objectCount ||
		source.Accepted < 1 ||
		source.Skipped < 0 ||
		len(source.Warnings) > MaxWarningSamples ||
		source.Skipped < len(source.Warnings) {
		return NormalizeInfo{}, errDiskStateInvalid
	}
	info := NormalizeInfo{
		Format:   format,
		Accepted: source.Accepted,
		Skipped:  source.Skipped,
		Warnings: make([]Warning, len(source.Warnings)),
	}
	for index, warning := range source.Warnings {
		if warning.NodeIndex < 1 ||
			safeWarningCode(warning.Code) != warning.Code ||
			(warning.Type != "" &&
				safeType(warning.Type) != warning.Type) ||
			(warning.Field != "" &&
				safeField(warning.Field) != warning.Field) {
			return NormalizeInfo{}, errDiskStateInvalid
		}
		info.Warnings[index] = Warning{
			Code:      warning.Code,
			NodeIndex: warning.NodeIndex,
			Type:      warning.Type,
			Field:     warning.Field,
		}
	}
	return info, nil
}

func diskStatusAgrees(
	status diskStatus,
	freshCount int,
	fallbackIndices []int,
) bool {
	if status.FreshCount != freshCount ||
		len(status.FallbackIndices) != len(fallbackIndices) {
		return false
	}
	for index := range fallbackIndices {
		if status.FallbackIndices[index] != fallbackIndices[index] ||
			(index > 0 &&
				status.FallbackIndices[index] <=
					status.FallbackIndices[index-1]) {
			return false
		}
	}
	if len(fallbackIndices) == 0 {
		return status.State == generationStateFresh
	}
	return status.State == generationStateDegraded
}

func diskDecodeManifest(raw []byte) (diskManifest, error) {
	root, err := diskParseStateJSON(raw)
	if err != nil || !diskValidateManifestShape(root) {
		return diskManifest{}, errDiskStateInvalid
	}
	var manifest diskManifest
	if diskDecodeTypedJSON(raw, &manifest) != nil {
		return diskManifest{}, errDiskStateInvalid
	}
	return manifest, nil
}

func diskDecodeStatus(raw []byte) (diskStatus, error) {
	root, err := diskParseStateJSON(raw)
	if err != nil || !diskValidateStatusShape(root) {
		return diskStatus{}, errDiskStateInvalid
	}
	var status diskStatus
	if diskDecodeTypedJSON(raw, &status) != nil {
		return diskStatus{}, errDiskStateInvalid
	}
	return status, nil
}

func diskDecodeTypedJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errDiskStateInvalid
	}
	return nil
}

func diskParseStateJSON(raw []byte) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > diskStateSchemaLimit {
		return nil, errDiskStateInvalid
	}
	value, err := diskParseJSON(raw)
	if err != nil {
		return nil, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errDiskStateInvalid
	}
	return root, nil
}

func diskValidateManifestShape(root map[string]any) bool {
	if !diskExactObjectKeys(root, []string{
		"schema", "generation", "parent", "config_digest",
		"aggregate", "status_sha256", "sources",
		"legacy_consumed_url_sha256",
	}) {
		return false
	}
	aggregate, ok := root["aggregate"].(map[string]any)
	if !ok || !diskExactObjectKeys(
		aggregate, []string{"sha256", "bytes", "outbounds"},
	) {
		return false
	}
	sources, ok := root["sources"].([]any)
	if !ok {
		return false
	}
	for _, value := range sources {
		source, ok := value.(map[string]any)
		if !ok || !diskExactObjectKeys(source, []string{
			"index", "url_sha256", "object_sha256", "bytes",
			"outbounds",
		}) {
			return false
		}
	}
	return true
}

func diskValidateStatusShape(root map[string]any) bool {
	if !diskExactObjectKeys(root, []string{
		"schema", "generation", "state", "fresh_count",
		"fallback_indices", "sources",
	}) {
		return false
	}
	if _, ok := root["fallback_indices"].([]any); !ok {
		return false
	}
	sources, ok := root["sources"].([]any)
	if !ok {
		return false
	}
	for _, value := range sources {
		source, ok := value.(map[string]any)
		if !ok || !diskExactObjectKeys(source, []string{
			"index", "result", "fetch_code", "format",
			"accepted", "skipped", "warnings",
		}) {
			return false
		}
		warnings, ok := source["warnings"].([]any)
		if !ok {
			return false
		}
		for _, warningValue := range warnings {
			warning, ok := warningValue.(map[string]any)
			if !ok || !diskExactObjectKeys(warning, []string{
				"code", "node_index", "type", "field",
			}) {
				return false
			}
		}
	}
	return true
}

func diskExactObjectKeys(
	object map[string]any,
	keys []string,
) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		value, exists := object[key]
		if !exists || value == nil {
			return false
		}
	}
	return true
}

func diskSHA256(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

type diskJSONParseState struct {
	nodes int
}

func diskParseJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) || !validJSONUnicodeEscapes(raw) {
		return nil, errDiskStateInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	state := &diskJSONParseState{}
	value, err := diskParseJSONValue(decoder, 0, state)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errDiskStateInvalid
	}
	return value, nil
}

func diskParseJSONValue(
	decoder *json.Decoder,
	depth int,
	state *diskJSONParseState,
) (any, error) {
	token, err := decoder.Token()
	if err != nil || !diskCountJSONToken(token, state) {
		return nil, errDiskStateInvalid
	}
	switch value := token.(type) {
	case json.Delim:
		if value != '{' && value != '[' ||
			depth >= MaxDocumentDepth {
			return nil, errDiskStateInvalid
		}
		switch value {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil ||
					!diskCountJSONToken(keyToken, state) {
					return nil, errDiskStateInvalid
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errDiskStateInvalid
				}
				if _, exists := object[key]; exists {
					return nil, errDiskStateInvalid
				}
				child, err := diskParseJSONValue(
					decoder, depth+1, state,
				)
				if err != nil {
					return nil, err
				}
				object[key] = child
			}
			closeToken, err := decoder.Token()
			if err != nil ||
				closeToken != json.Delim('}') ||
				!diskCountJSONToken(closeToken, state) {
				return nil, errDiskStateInvalid
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				child, err := diskParseJSONValue(
					decoder, depth+1, state,
				)
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			closeToken, err := decoder.Token()
			if err != nil ||
				closeToken != json.Delim(']') ||
				!diskCountJSONToken(closeToken, state) {
				return nil, errDiskStateInvalid
			}
			return array, nil
		default:
			return nil, errDiskStateInvalid
		}
	case json.Number:
		return value, nil
	case string, bool, nil:
		return value, nil
	default:
		return nil, errDiskStateInvalid
	}
}

func diskCountJSONToken(token any, state *diskJSONParseState) bool {
	state.nodes++
	if state.nodes > MaxYAMLNodes {
		return false
	}
	switch value := token.(type) {
	case string:
		return len(value) <= MaxScalarBytes
	case json.Number:
		return len(value.String()) <= MaxScalarBytes
	default:
		return true
	}
}
