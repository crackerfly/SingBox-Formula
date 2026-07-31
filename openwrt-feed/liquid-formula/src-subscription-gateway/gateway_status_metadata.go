package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	diskLastAttemptLimit           = 4096
	safeSubscriptionStatusLimit    = 32768
	safeSubscriptionSnapshotTries  = 8
	subscriptionOverallEmpty       = "empty"
	subscriptionOverallFailed      = "failed"
	subscriptionOverallUnavailable = "unavailable"
)

type failureStage string

const (
	failureStageConfiguration   failureStage = "configuration"
	failureStageCurrentState    failureStage = "current_state"
	failureStageSourceFetch     failureStage = "source_fetch"
	failureStageSourceNormalize failureStage = "source_normalize"
	failureStageAggregate       failureStage = "aggregate"
	failureStageCommit          failureStage = "commit"
	failureStageDeadline        failureStage = "deadline"
)

type diskLastAttempt struct {
	Schema           int    `json:"schema"`
	ConfigDigest     string `json:"config_digest"`
	ActiveGeneration string `json:"active_generation"`
	State            string `json:"state"`
	TotalSources     int    `json:"total_sources"`
	FailureStage     string `json:"failure_stage"`
	Code             string `json:"code"`
	FetchCode        string `json:"fetch_code"`
	SourceIndex      int    `json:"source_index"`
	Preserved        bool   `json:"preserved"`
}

type safeSubscriptionWarning struct {
	Code      string `json:"code"`
	NodeIndex int    `json:"node_index"`
	Type      string `json:"type"`
	Field     string `json:"field"`
}

type safeSubscriptionSource struct {
	Index     int                       `json:"index"`
	Result    string                    `json:"result"`
	FetchCode string                    `json:"fetch_code"`
	Format    string                    `json:"format"`
	Accepted  int                       `json:"accepted"`
	Skipped   int                       `json:"skipped"`
	Warnings  []safeSubscriptionWarning `json:"warnings"`
}

type safeLastAttempt struct {
	State        string `json:"state"`
	TotalSources int    `json:"total_sources"`
	FailureStage string `json:"failure_stage"`
	Code         string `json:"code"`
	FetchCode    string `json:"fetch_code"`
	SourceIndex  int    `json:"source_index"`
	Preserved    bool   `json:"preserved"`
}

type safeSubscriptionStatus struct {
	Schema           int                      `json:"schema"`
	OverallState     string                   `json:"overall_state"`
	ConfigMatch      bool                     `json:"config_match"`
	ActiveGeneration string                   `json:"active_generation"`
	TotalSources     int                      `json:"total_sources"`
	FreshCount       int                      `json:"fresh_count"`
	FallbackIndices  []int                    `json:"fallback_indices"`
	Sources          []safeSubscriptionSource `json:"sources"`
	LastAttempt      *safeLastAttempt         `json:"last_attempt"`
}

type diskLastAttemptRecorder struct {
	root       string
	filesystem diskGenerationFilesystem
	faultHook  func(string) error
	mu         sync.Mutex
}

type attemptRecordingEngine struct {
	aggregateEngine
	recorder *diskLastAttemptRecorder
}

func (engine *attemptRecordingEngine) RecordFailure(
	config gatewayConfig,
	outcome aggregateOutcome,
) error {
	if engine == nil || engine.recorder == nil {
		return errDiskStateInvalid
	}
	return engine.recorder.RecordFailure(config, outcome)
}

func newDiskLastAttemptRecorder(root string) *diskLastAttemptRecorder {
	return &diskLastAttemptRecorder{
		root:       root,
		filesystem: nativeDiskGenerationFilesystem{},
		faultHook:  func(string) error { return nil },
	}
}

func (recorder *diskLastAttemptRecorder) RecordFailure(
	config gatewayConfig,
	outcome aggregateOutcome,
) error {
	if outcome.Code == aggregateCodeBusy {
		return nil
	}
	if recorder == nil || recorder.root == "" || recorder.filesystem == nil ||
		recorder.faultHook == nil || !isLowerHexDigest(config.ConfigDigest) ||
		len(config.Sources) > 8 {
		return errDiskStateInvalid
	}
	// Empty ActiveGeneration is a meaningful transaction result: the request
	// observed that no generation existed. Never reread current here, after the
	// transaction lock has been released, because a later successful request
	// could otherwise make this older failure appear to belong to its generation.
	if !outcome.CurrentObserved {
		return errDiskStateInvalid
	}
	stage := outcome.FailureStage
	if stage == "" {
		stage = defaultFailureStage(outcome)
	}
	if !validLastAttemptOutcome(outcome, stage, len(config.Sources)) {
		return errDiskStateInvalid
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	root, err := diskOpenDirectoryPath(recorder.root)
	if err != nil {
		return errDiskStateInvalid
	}
	defer root.Close()
	active := outcome.ActiveGeneration
	if active != "" && !isLowerHexDigest(active) {
		return errDiskStateInvalid
	}
	record := diskLastAttempt{
		Schema:           1,
		ConfigDigest:     config.ConfigDigest,
		ActiveGeneration: active,
		State:            subscriptionOverallFailed,
		TotalSources:     len(config.Sources),
		FailureStage:     string(stage),
		Code:             string(outcome.Code),
		FetchCode:        string(outcome.FetchCode),
		SourceIndex:      outcome.SourceIndex,
		Preserved:        outcome.Preserved,
	}
	if !validDiskLastAttempt(record) {
		return errDiskStateInvalid
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) == 0 || len(raw) > diskLastAttemptLimit {
		return errDiskStateInvalid
	}
	temporary := diskPrivateName(".last-attempt-")
	temporaryPresent := false
	defer func() {
		if temporaryPresent {
			_ = unix.Unlinkat(int(root.Fd()), temporary, 0)
		}
	}()
	if diskWriteAndSyncFile(
		root, temporary, raw, recorder.filesystem,
	) != nil {
		return errDiskStateInvalid
	}
	temporaryPresent = true
	staged, missing, err := diskReadSecureFileAt(
		root, temporary, diskLastAttemptLimit,
	)
	if err != nil || missing || !bytes.Equal(staged, raw) ||
		recorder.faultHook("before_last_attempt_rename") != nil {
		return errDiskStateInvalid
	}
	if recorder.filesystem.RenameReplace(
		root, temporary, root, "last-attempt.json",
	) != nil {
		return errDiskStateInvalid
	}
	temporaryPresent = false
	if recorder.filesystem.Sync(root) != nil {
		return errDiskStateInvalid
	}
	return nil
}

func defaultFailureStage(outcome aggregateOutcome) failureStage {
	switch outcome.Code {
	case aggregateCodeNoSources:
		return failureStageConfiguration
	case aggregateCodeSourceUnavailable:
		if outcome.FetchCode == fetchCodeNormalize {
			return failureStageSourceNormalize
		}
		return failureStageSourceFetch
	case aggregateCodeAggregateInvalid:
		return failureStageAggregate
	case aggregateCodeCommitFailed:
		return failureStageCommit
	default:
		return failureStageCurrentState
	}
}

func validFailureStage(stage failureStage) bool {
	switch stage {
	case failureStageConfiguration,
		failureStageCurrentState,
		failureStageSourceFetch,
		failureStageSourceNormalize,
		failureStageAggregate,
		failureStageCommit,
		failureStageDeadline:
		return true
	default:
		return false
	}
}

func validAggregateFailureCode(code aggregateFailureCode) bool {
	switch code {
	case aggregateCodeNoSources,
		aggregateCodeSourceUnavailable,
		aggregateCodeStateInvalid,
		aggregateCodeAggregateInvalid,
		aggregateCodeCommitFailed:
		return true
	default:
		return false
	}
}

func validLastAttemptOutcome(
	outcome aggregateOutcome,
	stage failureStage,
	totalSources int,
) bool {
	if totalSources < 0 || totalSources > 8 ||
		!validAggregateFailureCode(outcome.Code) ||
		!validFailureStage(stage) ||
		(outcome.SourceIndex < 0 || outcome.SourceIndex > totalSources) {
		return false
	}
	if outcome.Code == aggregateCodeSourceUnavailable {
		if outcome.SourceIndex < 1 ||
			!validFetchFailureCode(outcome.FetchCode) {
			return false
		}
		if stage == failureStageSourceNormalize {
			return outcome.FetchCode == fetchCodeNormalize
		}
		return stage == failureStageSourceFetch &&
			outcome.FetchCode != fetchCodeNormalize
	}
	if outcome.SourceIndex != 0 || outcome.FetchCode != "" {
		return false
	}
	switch outcome.Code {
	case aggregateCodeNoSources:
		return totalSources == 0 && stage == failureStageConfiguration
	case aggregateCodeStateInvalid:
		switch stage {
		case failureStageConfiguration,
			failureStageCurrentState,
			failureStageCommit,
			failureStageDeadline:
			return true
		default:
			return false
		}
	case aggregateCodeAggregateInvalid:
		return stage == failureStageAggregate
	case aggregateCodeCommitFailed:
		return stage == failureStageCommit
	default:
		return false
	}
}

func validDiskLastAttempt(record diskLastAttempt) bool {
	return record.Schema == 1 &&
		isLowerHexDigest(record.ConfigDigest) &&
		(record.ActiveGeneration == "" ||
			isLowerHexDigest(record.ActiveGeneration)) &&
		(!record.Preserved || record.ActiveGeneration != "") &&
		record.State == subscriptionOverallFailed &&
		validLastAttemptOutcome(aggregateOutcome{
			Code:         aggregateFailureCode(record.Code),
			SourceIndex:  record.SourceIndex,
			Preserved:    record.Preserved,
			FailureStage: failureStage(record.FailureStage),
			FetchCode:    sourceFetchCode(record.FetchCode),
		}, failureStage(record.FailureStage), record.TotalSources)
}

func diskReadCurrentGenerationID(root *os.File) (string, error) {
	raw, missing, err := diskReadSecureFileAt(
		root, "current", diskCurrentBytes,
	)
	if err != nil {
		return "", err
	}
	if missing {
		return "", nil
	}
	if len(raw) != diskCurrentBytes || raw[len(raw)-1] != '\n' {
		return "", errDiskStateInvalid
	}
	generation := string(raw[:len(raw)-1])
	if !isLowerHexDigest(generation) {
		return "", errDiskStateInvalid
	}
	return generation, nil
}

type safeSelectedMetadata struct {
	present  bool
	manifest diskManifest
	status   diskStatus
}

func readSafeSubscriptionStatus(
	rootPath string,
	expectedConfigDigest string,
	totalSources int,
) safeSubscriptionStatus {
	return readSafeSubscriptionStatusWithHook(
		rootPath, expectedConfigDigest, totalSources, nil,
	)
}

func readSafeSubscriptionStatusWithHook(
	rootPath string,
	expectedConfigDigest string,
	totalSources int,
	hook func(string),
) safeSubscriptionStatus {
	result := emptySafeSubscriptionStatus(totalSources)
	if rootPath == "" || !isLowerHexDigest(expectedConfigDigest) ||
		totalSources < 0 || totalSources > 8 {
		return unavailableSafeSubscriptionStatus(totalSources)
	}
	for attempt := 0; attempt < safeSubscriptionSnapshotTries; attempt++ {
		selected, lastAttempt, stable, absent, err :=
			readSafeSubscriptionSnapshot(rootPath, hook)
		if errors.Is(err, os.ErrNotExist) || absent {
			return result
		}
		if err != nil {
			continue
		}
		if !stable {
			continue
		}
		if selected.present {
			result.ActiveGeneration = selected.manifest.Generation
			result.ConfigMatch = selected.manifest.ConfigDigest ==
				expectedConfigDigest
			if result.ConfigMatch {
				if len(selected.status.Sources) != totalSources {
					return unavailableSafeSubscriptionStatus(totalSources)
				}
				result.OverallState = selected.status.State
				result.FreshCount = selected.status.FreshCount
				result.FallbackIndices = append(
					[]int(nil), selected.status.FallbackIndices...,
				)
				result.Sources = safeSourcesFromDisk(
					selected.status.Sources,
				)
			}
		}
		if lastAttempt != nil &&
			lastAttempt.ConfigDigest == expectedConfigDigest &&
			lastAttempt.ActiveGeneration == result.ActiveGeneration {
			if lastAttempt.TotalSources != totalSources {
				return unavailableSafeSubscriptionStatus(totalSources)
			}
			result.OverallState = subscriptionOverallFailed
			result.LastAttempt = &safeLastAttempt{
				State:        lastAttempt.State,
				TotalSources: lastAttempt.TotalSources,
				FailureStage: lastAttempt.FailureStage,
				Code:         lastAttempt.Code,
				FetchCode:    lastAttempt.FetchCode,
				SourceIndex:  lastAttempt.SourceIndex,
				Preserved:    lastAttempt.Preserved,
			}
		}
		return result
	}
	return unavailableSafeSubscriptionStatus(totalSources)
}

func emptySafeSubscriptionStatus(totalSources int) safeSubscriptionStatus {
	if totalSources < 0 || totalSources > 8 {
		totalSources = 0
	}
	return safeSubscriptionStatus{
		Schema:          1,
		OverallState:    subscriptionOverallEmpty,
		TotalSources:    totalSources,
		FallbackIndices: []int{},
		Sources:         []safeSubscriptionSource{},
	}
}

func unavailableSafeSubscriptionStatus(totalSources int) safeSubscriptionStatus {
	status := emptySafeSubscriptionStatus(totalSources)
	status.OverallState = subscriptionOverallUnavailable
	return status
}

func safeSourcesFromDisk(
	sources []diskStatusSource,
) []safeSubscriptionSource {
	result := make([]safeSubscriptionSource, len(sources))
	for offset, source := range sources {
		warnings := make(
			[]safeSubscriptionWarning, len(source.Warnings),
		)
		for index, warning := range source.Warnings {
			warnings[index] = safeSubscriptionWarning{
				Code:      warning.Code,
				NodeIndex: warning.NodeIndex,
				Type:      warning.Type,
				Field:     warning.Field,
			}
		}
		result[offset] = safeSubscriptionSource{
			Index:     source.Index,
			Result:    source.Result,
			FetchCode: source.FetchCode,
			Format:    source.Format,
			Accepted:  source.Accepted,
			Skipped:   source.Skipped,
			Warnings:  warnings,
		}
	}
	return result
}

func readSafeSubscriptionSnapshot(
	rootPath string,
	hook func(string),
) (safeSelectedMetadata, *diskLastAttempt, bool, bool, error) {
	root, err := diskOpenDirectoryPath(rootPath)
	if errors.Is(err, os.ErrNotExist) {
		return safeSelectedMetadata{}, nil, true, true, nil
	}
	if err != nil {
		return safeSelectedMetadata{}, nil, false, false,
			errDiskStateInvalid
	}
	defer root.Close()
	generations, err := diskOpenDirectoryAt(root, "generations")
	if err != nil {
		return safeSelectedMetadata{}, nil, false, false,
			errDiskStateInvalid
	}
	defer generations.Close()
	objects, err := diskOpenDirectoryAt(root, "objects")
	if err != nil {
		return safeSelectedMetadata{}, nil, false, false,
			errDiskStateInvalid
	}
	defer objects.Close()

	before, err := diskReadCurrentGenerationID(root)
	if err != nil {
		return safeSelectedMetadata{}, nil, false, false, err
	}
	selected := safeSelectedMetadata{}
	if before != "" {
		manifest, status, err := diskLoadSafeGenerationMetadata(
			generations, objects, before,
		)
		if err != nil {
			return safeSelectedMetadata{}, nil, false, false, err
		}
		selected = safeSelectedMetadata{
			present: true, manifest: manifest, status: status,
		}
	}
	if hook != nil {
		hook("after_selected_metadata")
	}
	lastAttempt, err := diskReadLastAttempt(root)
	if err != nil {
		return safeSelectedMetadata{}, nil, false, false, err
	}
	after, err := diskReadCurrentGenerationID(root)
	if err != nil {
		return safeSelectedMetadata{}, nil, false, false, err
	}
	return selected, lastAttempt, before == after, false, nil
}

func diskLoadSafeGenerationMetadata(
	generations *os.File,
	objects *os.File,
	generationID string,
) (diskManifest, diskStatus, error) {
	if !isLowerHexDigest(generationID) {
		return diskManifest{}, diskStatus{}, errDiskStateInvalid
	}
	directory, err := diskOpenDirectoryAt(generations, generationID)
	if err != nil {
		return diskManifest{}, diskStatus{}, errDiskStateInvalid
	}
	defer directory.Close()
	if !diskGenerationHasExactFiles(directory) {
		return diskManifest{}, diskStatus{}, errDiskStateInvalid
	}
	manifestRaw, missing, err := diskReadSecureFileAt(
		directory, "manifest.json", diskStateSchemaLimit,
	)
	if err != nil || missing {
		return diskManifest{}, diskStatus{}, errDiskStateInvalid
	}
	statusRaw, missing, err := diskReadSecureFileAt(
		directory, "status.json", diskStateSchemaLimit,
	)
	if err != nil || missing {
		return diskManifest{}, diskStatus{}, errDiskStateInvalid
	}
	manifest, err := diskDecodeManifest(manifestRaw)
	if err != nil {
		return diskManifest{}, diskStatus{}, errDiskStateInvalid
	}
	status, err := diskDecodeStatus(statusRaw)
	if err != nil ||
		!diskValidManifestHeader(manifest, generationID) ||
		status.Schema != 1 || status.Generation != generationID ||
		diskSHA256(statusRaw) != manifest.StatusSHA256 ||
		len(manifest.Sources) < 1 || len(manifest.Sources) > 8 ||
		len(status.Sources) != len(manifest.Sources) {
		return diskManifest{}, diskStatus{}, errDiskStateInvalid
	}
	aggregateSize, missing, err := diskSecureFileSizeAt(
		directory, "aggregate.json", canonicalAggregateByteLimit,
	)
	if err != nil || missing || aggregateSize != manifest.Aggregate.Bytes {
		return diskManifest{}, diskStatus{}, errDiskStateInvalid
	}

	freshCount := 0
	fallbackIndices := make([]int, 0)
	type duplicateMetadata struct {
		object string
		bytes  int
		count  int
		result string
		fetch  string
		info   NormalizeInfo
	}
	duplicates := make(map[string]duplicateMetadata)
	objectSizes := make(map[string]int)
	for offset := range manifest.Sources {
		manifestSource := manifest.Sources[offset]
		statusSource := status.Sources[offset]
		index := offset + 1
		if manifestSource.Index != index || statusSource.Index != index ||
			!isLowerHexDigest(manifestSource.URLSHA256) ||
			!isLowerHexDigest(manifestSource.ObjectSHA256) ||
			manifestSource.Bytes < 1 ||
			manifestSource.Bytes > canonicalAggregateByteLimit ||
			manifestSource.Outbounds < 1 ||
			manifestSource.Outbounds > MaxNormalizedNodes {
			return diskManifest{}, diskStatus{}, errDiskStateInvalid
		}
		objectSize, loaded := objectSizes[manifestSource.ObjectSHA256]
		if !loaded {
			objectSize, missing, err = diskSecureFileSizeAt(
				objects,
				manifestSource.ObjectSHA256+".json",
				canonicalAggregateByteLimit,
			)
			if err != nil || missing {
				return diskManifest{}, diskStatus{}, errDiskStateInvalid
			}
			objectSizes[manifestSource.ObjectSHA256] = objectSize
		}
		if objectSize != manifestSource.Bytes {
			return diskManifest{}, diskStatus{}, errDiskStateInvalid
		}
		info, err := diskValidateStatusSource(
			statusSource, manifestSource.Outbounds,
		)
		if err != nil {
			return diskManifest{}, diskStatus{}, errDiskStateInvalid
		}
		switch statusSource.Result {
		case sourceResultFresh:
			if statusSource.FetchCode != string(fetchCodeOK) {
				return diskManifest{}, diskStatus{}, errDiskStateInvalid
			}
			freshCount++
		case sourceResultFallback:
			if !validFetchFailureCode(sourceFetchCode(statusSource.FetchCode)) {
				return diskManifest{}, diskStatus{}, errDiskStateInvalid
			}
			fallbackIndices = append(fallbackIndices, index)
		default:
			return diskManifest{}, diskStatus{}, errDiskStateInvalid
		}
		metadata := duplicateMetadata{
			object: manifestSource.ObjectSHA256,
			bytes:  manifestSource.Bytes,
			count:  manifestSource.Outbounds,
			result: statusSource.Result,
			fetch:  statusSource.FetchCode,
			info:   info,
		}
		if previous, exists := duplicates[manifestSource.URLSHA256]; exists {
			if previous.object != metadata.object ||
				previous.bytes != metadata.bytes ||
				previous.count != metadata.count ||
				previous.result != metadata.result ||
				previous.fetch != metadata.fetch ||
				!equalNormalizeInfo(previous.info, metadata.info) {
				return diskManifest{}, diskStatus{}, errDiskStateInvalid
			}
		} else {
			duplicates[manifestSource.URLSHA256] = metadata
		}
	}
	if !diskStatusAgrees(status, freshCount, fallbackIndices) {
		return diskManifest{}, diskStatus{}, errDiskStateInvalid
	}
	return manifest, status, nil
}

func diskSecureFileSizeAt(
	directory *os.File,
	name string,
	limit int,
) (int, bool, error) {
	fd, err := unix.Openat(
		int(directory.Fd()), name,
		unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if errors.Is(err, unix.ENOENT) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, errDiskStateInvalid
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&07777 != 0600 || stat.Nlink != 1 ||
		stat.Size < 1 || stat.Size > int64(limit) {
		return 0, false, errDiskStateInvalid
	}
	return int(stat.Size), false, nil
}

func diskReadLastAttempt(root *os.File) (*diskLastAttempt, error) {
	raw, missing, err := diskReadSecureFileAt(
		root, "last-attempt.json", diskLastAttemptLimit,
	)
	if err != nil {
		return nil, err
	}
	if missing {
		return nil, nil
	}
	parsed, err := diskParseJSON(raw)
	if err != nil {
		return nil, errDiskStateInvalid
	}
	object, ok := parsed.(map[string]any)
	if !ok || !diskExactObjectKeys(object, []string{
		"schema", "config_digest", "active_generation", "state",
		"total_sources", "failure_stage", "code", "fetch_code",
		"source_index", "preserved",
	}) {
		return nil, errDiskStateInvalid
	}
	var record diskLastAttempt
	if diskDecodeTypedJSON(raw, &record) != nil ||
		!validDiskLastAttempt(record) {
		return nil, errDiskStateInvalid
	}
	return &record, nil
}

type statusDependencies struct {
	ReadFile  func(string) ([]byte, error)
	StateRoot string
}

func defaultStatusDependencies() statusDependencies {
	return statusDependencies{
		ReadFile:  os.ReadFile,
		StateRoot: productionSubscriptionStateRoot,
	}
}

func runStatus(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	dependencies statusDependencies,
) int {
	flags := newSilentFlagSet("status")
	var configPath requiredStringFlag
	var expectedDigest requiredStringFlag
	flags.Var(&configPath, "config", "")
	flags.Var(&expectedDigest, "expected-digest", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 ||
		!configPath.set || configPath.value == "" ||
		!expectedDigest.set || !isLowerHexDigest(expectedDigest.value) ||
		dependencies.ReadFile == nil || dependencies.StateRoot == "" {
		writeGatewayDiagnostic(stderr, "usage_error")
		return 2
	}
	config, err := readGatewayConfig(
		configPath.value,
		expectedDigest.value,
		dependencies.ReadFile,
	)
	if err != nil {
		var configError *gatewayConfigError
		if errors.As(err, &configError) {
			writeGatewayDiagnostic(stderr, configError.code)
		} else {
			writeGatewayDiagnostic(stderr, "config_invalid")
		}
		return 1
	}
	status := readSafeSubscriptionStatus(
		dependencies.StateRoot,
		config.ConfigDigest,
		len(config.Sources),
	)
	raw, err := json.Marshal(status)
	if err != nil || len(raw) == 0 ||
		len(raw) > safeSubscriptionStatusLimit {
		fmt.Fprintln(stderr,
			"liquid-formula-subscription-gateway: code=status_output_failed")
		return 1
	}
	raw = append(raw, '\n')
	if _, err := stdout.Write(raw); err != nil {
		fmt.Fprintln(stderr,
			"liquid-formula-subscription-gateway: code=status_output_failed")
		return 1
	}
	return 0
}

func newSilentFlagSet(name string) *silentFlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

type silentFlagSet = flag.FlagSet
