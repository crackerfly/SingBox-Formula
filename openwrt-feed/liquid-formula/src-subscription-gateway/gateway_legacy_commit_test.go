package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiskCommitPersistsConsumptionThenRemovesMarkerAfterRootSync(
	t *testing.T,
) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	root, nodePath := legacyTestPrepareProviderRoot(t)
	legacyTestWriteMarker(t, root, digest+"\n")
	delegate := newFileLegacySourceProvider(root, nodePath)
	probe, err := delegate.Probe(
		context.Background(),
		legacyProbeRequest{
			Selection:      currentSelection{Kind: currentAbsent},
			FirstURLDigest: digest,
		},
	)
	if err != nil || !probe.Eligible {
		t.Fatalf("probe = %#v err=%v", probe, err)
	}

	events := &legacyTestEvents{}
	provider := &legacyTestRecordingProvider{
		delegate: delegate,
		events:   events,
	}
	filesystem := &legacyTestFilesystem{
		root: root, events: events,
	}
	store := newDiskGenerationStoreWithDependencies(
		root,
		diskGenerationStoreDependencies{
			GenerationRandom: legacyTestRandom(0x11),
			FS:               filesystem,
			Legacy:           provider,
		},
	)
	result, commitErr, gate := legacyTestCommit(
		store,
		legacyTestCandidate(
			"", digest, digest, &probe.Token, "First",
		),
	)
	if !result.Committed || !gate.begun() {
		t.Fatalf(
			"commit failed: result=%#v err=%v begun=%t",
			result, commitErr, gate.begun(),
		)
	}
	if _, err := os.Lstat(legacyTestMarkerPath(root)); !os.IsNotExist(err) {
		t.Fatalf("committed marker still exists: %v", err)
	}
	if provider.removeCalls != 1 ||
		provider.removeDigest != digest {
		t.Fatalf(
			"cleanup calls=%d digest=%q",
			provider.removeCalls, provider.removeDigest,
		)
	}
	if !legacyTestOrdered(
		events.snapshot(),
		"rename-current",
		"root-sync-after-current",
		"remove-marker",
	) {
		t.Fatalf("commit/cleanup order = %v", events.snapshot())
	}

	selection := result.Selection
	if selection.Kind != currentPresent ||
		selection.Generation.LegacyConsumedURLDigest != digest {
		t.Fatalf("committed selection = %#v", selection)
	}
	manifestPath := filepath.Join(
		root,
		"generations",
		selection.Generation.GenerationID,
		"manifest.json",
	)
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		LegacyConsumedURLDigest string `json:"legacy_consumed_url_sha256"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("manifest decode: %v", err)
	}
	if manifest.LegacyConsumedURLDigest != digest {
		t.Fatalf(
			"manifest legacy digest = %q, want %q",
			manifest.LegacyConsumedURLDigest, digest,
		)
	}
	statusBytes, err := os.ReadFile(filepath.Join(
		root,
		"generations",
		selection.Generation.GenerationID,
		"status.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{root, nodePath} {
		if bytes.Contains(manifestBytes, []byte(forbidden)) ||
			bytes.Contains(statusBytes, []byte(forbidden)) {
			t.Fatalf(
				"persisted state leaked legacy path %q: manifest=%s status=%s",
				forbidden, manifestBytes, statusBytes,
			)
		}
	}
}

func TestDiskCommitNeverRemovesLegacyMarkerBeforeDurableCurrent(
	t *testing.T,
) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	t.Run("pre-current failure retains marker", func(t *testing.T) {
		root, nodePath := legacyTestPrepareProviderRoot(t)
		legacyTestWriteMarker(t, root, digest+"\n")
		delegate := newFileLegacySourceProvider(root, nodePath)
		probe, err := delegate.Probe(
			context.Background(),
			legacyProbeRequest{
				Selection:      currentSelection{Kind: currentAbsent},
				FirstURLDigest: digest,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		provider := &legacyTestRecordingProvider{delegate: delegate}
		store := newDiskGenerationStoreWithDependencies(
			root,
			diskGenerationStoreDependencies{
				GenerationRandom: legacyTestRandom(0x21),
				FS: &legacyTestFilesystem{
					root: root,
				},
				FaultHook: func(stage string) error {
					if stage == "before_current_rename" {
						return errors.New("injected pre-current failure")
					}
					return nil
				},
				Legacy: provider,
			},
		)
		result, _, gate := legacyTestCommit(
			store,
			legacyTestCandidate(
				"", digest, digest, &probe.Token, "PreCurrent",
			),
		)
		if result.Committed || gate.begun() ||
			provider.removeCalls != 0 {
			t.Fatalf(
				"pre-current result=%#v begun=%t removes=%d",
				result, gate.begun(), provider.removeCalls,
			)
		}
		legacyTestRequireMarker(t, root, digest)
	})

	t.Run("post-rename root sync failure retains marker", func(t *testing.T) {
		root, nodePath := legacyTestPrepareProviderRoot(t)
		legacyTestWriteMarker(t, root, digest+"\n")
		delegate := newFileLegacySourceProvider(root, nodePath)
		probe, err := delegate.Probe(
			context.Background(),
			legacyProbeRequest{
				Selection:      currentSelection{Kind: currentAbsent},
				FirstURLDigest: digest,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		provider := &legacyTestRecordingProvider{delegate: delegate}
		filesystem := &legacyTestFilesystem{
			root:                     root,
			failRootSyncAfterCurrent: true,
		}
		store := newDiskGenerationStoreWithDependencies(
			root,
			diskGenerationStoreDependencies{
				GenerationRandom: legacyTestRandom(0x22),
				FS:               filesystem,
				Legacy:           provider,
			},
		)
		result, commitErr, gate := legacyTestCommit(
			store,
			legacyTestCandidate(
				"", digest, digest, &probe.Token, "RootSync",
			),
		)
		if !result.Committed || !gate.begun() ||
			result.WarningCode != "current_dir_sync_failed" ||
			provider.removeCalls != 0 {
			t.Fatalf(
				"root-sync result=%#v err=%v begun=%t removes=%d",
				result, commitErr, gate.begun(), provider.removeCalls,
			)
		}
		legacyTestRequireMarker(t, root, digest)
		wantCurrent := result.Selection.Generation.GenerationID + "\n"
		current, err := os.ReadFile(filepath.Join(root, "current"))
		if err != nil || string(current) != wantCurrent {
			t.Fatalf("logical current=%q err=%v", current, err)
		}
	})
}

func TestDiskCommitMarkerCleanupIsIdentityBoundAndBestEffort(
	t *testing.T,
) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	t.Run("replacement marker is never deleted", func(t *testing.T) {
		root, nodePath := legacyTestPrepareProviderRoot(t)
		legacyTestWriteMarker(t, root, digest+"\n")
		delegate := newFileLegacySourceProvider(root, nodePath)
		probe, err := delegate.Probe(
			context.Background(),
			legacyProbeRequest{
				Selection:      currentSelection{Kind: currentAbsent},
				FirstURLDigest: digest,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		provider := &legacyTestRecordingProvider{delegate: delegate}
		var replacementIdentity legacyTestFileIdentity
		store := newDiskGenerationStoreWithDependencies(
			root,
			diskGenerationStoreDependencies{
				GenerationRandom: legacyTestRandom(0x31),
				FS: &legacyTestFilesystem{
					root: root,
				},
				FaultHook: func(stage string) error {
					if stage != "after_current_rename_before_root_sync" {
						return nil
					}
					replacement := filepath.Join(
						root, "replacement-marker",
					)
					if err := os.WriteFile(
						replacement, []byte(digest+"\n"), 0600,
					); err != nil {
						return err
					}
					if err := os.Rename(
						replacement, legacyTestMarkerPath(root),
					); err != nil {
						return err
					}
					identity, err := legacyTestIdentity(
						legacyTestMarkerPath(root),
					)
					if err != nil {
						return err
					}
					replacementIdentity = identity
					return nil
				},
				Legacy: provider,
			},
		)
		result, commitErr, gate := legacyTestCommit(
			store,
			legacyTestCandidate(
				"", digest, digest, &probe.Token, "Replacement",
			),
		)
		if !result.Committed || !gate.begun() ||
			provider.removeCalls != 1 {
			t.Fatalf(
				"replacement result=%#v err=%v begun=%t removes=%d",
				result, commitErr, gate.begun(), provider.removeCalls,
			)
		}
		legacyTestRequireMarker(t, root, digest)
		afterIdentity, err := legacyTestIdentity(
			legacyTestMarkerPath(root),
		)
		if err != nil || afterIdentity != replacementIdentity {
			t.Fatalf(
				"replacement identity changed: before=%#v after=%#v err=%v",
				replacementIdentity, afterIdentity, err,
			)
		}
	})

	t.Run("cleanup error cannot undo committed success", func(t *testing.T) {
		root, nodePath := legacyTestPrepareProviderRoot(t)
		legacyTestWriteMarker(t, root, digest+"\n")
		delegate := newFileLegacySourceProvider(root, nodePath)
		probe, err := delegate.Probe(
			context.Background(),
			legacyProbeRequest{
				Selection:      currentSelection{Kind: currentAbsent},
				FirstURLDigest: digest,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		provider := &legacyTestRecordingProvider{
			delegate: delegate,
			removeErr: errors.New(
				"LEGACY_REMOVE_SECRET_CANARY",
			),
		}
		store := newDiskGenerationStoreWithDependencies(
			root,
			diskGenerationStoreDependencies{
				GenerationRandom: legacyTestRandom(0x32),
				FS: &legacyTestFilesystem{
					root: root,
				},
				Legacy: provider,
			},
		)
		result, commitErr, gate := legacyTestCommit(
			store,
			legacyTestCandidate(
				"", digest, digest, &probe.Token, "RemoveFailure",
			),
		)
		if !result.Committed || !gate.begun() ||
			provider.removeCalls != 1 {
			t.Fatalf(
				"cleanup-failure result=%#v err=%v begun=%t removes=%d",
				result, commitErr, gate.begun(), provider.removeCalls,
			)
		}
		legacyTestRequireMarker(t, root, digest)
	})

	t.Run("cancelled cleanup context cannot undo committed success", func(t *testing.T) {
		root, nodePath := legacyTestPrepareProviderRoot(t)
		legacyTestWriteMarker(t, root, digest+"\n")
		delegate := newFileLegacySourceProvider(root, nodePath)
		probe, err := delegate.Probe(
			context.Background(),
			legacyProbeRequest{
				Selection:      currentSelection{Kind: currentAbsent},
				FirstURLDigest: digest,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		provider := &legacyTestRecordingProvider{
			delegate:        delegate,
			failOnCancelled: true,
		}
		filesystem := &legacyTestFilesystem{
			root: root,
			afterRootSync: func() {
				cancel()
			},
		}
		store := newDiskGenerationStoreWithDependencies(
			root,
			diskGenerationStoreDependencies{
				GenerationRandom: legacyTestRandom(0x33),
				FS:               filesystem,
				Legacy:           provider,
			},
		)
		ctx, gate := withNewCurrentCommitGate(ctx)
		result, commitErr := store.Commit(
			ctx,
			legacyTestCandidate(
				"", digest, digest, &probe.Token, "CancelledCleanup",
			),
		)
		if !result.Committed || !gate.begun() ||
			provider.removeCalls != 1 ||
			provider.removeCtx == nil ||
			provider.removeCtx.Err() == nil {
			t.Fatalf(
				"cancelled-cleanup result=%#v err=%v begun=%t removes=%d ctx=%v",
				result,
				commitErr,
				gate.begun(),
				provider.removeCalls,
				provider.removeCtx,
			)
		}
		legacyTestRequireMarker(t, root, digest)
	})
}

func TestDiskCommitEnforcesMonotonicLegacyConsumption(t *testing.T) {
	const (
		digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		digestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	root, nodePath := legacyTestPrepareProviderRoot(t)
	legacyTestWriteMarker(t, root, digestA+"\n")
	provider := newFileLegacySourceProvider(root, nodePath)
	probe, err := provider.Probe(
		context.Background(),
		legacyProbeRequest{
			Selection:      currentSelection{Kind: currentAbsent},
			FirstURLDigest: digestA,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	store := newDiskGenerationStoreWithDependencies(
		root,
		diskGenerationStoreDependencies{
			GenerationRandom: legacyTestRandom(
				0x41, 0x42, 0x43, 0x44,
			),
			FS: &legacyTestFilesystem{
				root: root,
			},
			Legacy: provider,
		},
	)

	firstResult, firstErr, _ := legacyTestCommit(
		store,
		legacyTestCandidate(
			"", digestA, digestA, &probe.Token, "First",
		),
	)
	if !firstResult.Committed {
		t.Fatalf("first commit: result=%#v err=%v", firstResult, firstErr)
	}
	if firstResult.Selection.Generation.LegacyConsumedURLDigest != digestA {
		t.Fatalf("first selection = %#v", firstResult.Selection)
	}

	secondResult, secondErr, _ := legacyTestCommit(
		store,
		legacyTestCandidate(
			firstResult.Selection.Generation.GenerationID,
			digestA,
			digestA,
			nil,
			"Second",
		),
	)
	if !secondResult.Committed {
		t.Fatalf(
			"same digest did not propagate: result=%#v err=%v",
			secondResult, secondErr,
		)
	}

	oldCurrent, err := os.ReadFile(filepath.Join(root, "current"))
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name     string
		consumed string
	}{
		{name: "missing inherited value", consumed: ""},
		{name: "conflicting inherited value", consumed: digestB},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, _, gate := legacyTestCommit(
				store,
				legacyTestCandidate(
					secondResult.Selection.Generation.GenerationID,
					digestA,
					testCase.consumed,
					nil,
					"Conflict",
				),
			)
			if result.Committed || gate.begun() {
				t.Fatalf(
					"invalid monotonic commit=%#v begun=%t",
					result, gate.begun(),
				)
			}
			current, err := os.ReadFile(filepath.Join(root, "current"))
			if err != nil || !bytes.Equal(current, oldCurrent) {
				t.Fatalf(
					"current changed: got=%q want=%q err=%v",
					current, oldCurrent, err,
				)
			}
		})
	}
}

func TestDiskCommitRejectsNewConsumptionWithoutProbeReceipt(
	t *testing.T,
) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	root, _ := legacyTestPrepareProviderRoot(t)
	store := newDiskGenerationStoreWithDependencies(
		root,
		diskGenerationStoreDependencies{
			GenerationRandom: legacyTestRandom(0x51),
			FS: &legacyTestFilesystem{
				root: root,
			},
			Legacy: &legacyTestProvider{},
		},
	)
	result, _, gate := legacyTestCommit(
		store,
		legacyTestCandidate("", digest, digest, nil, "Forged"),
	)
	if result.Committed || gate.begun() {
		t.Fatalf(
			"unproven consumption committed: result=%#v begun=%t",
			result, gate.begun(),
		)
	}
}

type legacyTestRecordingProvider struct {
	delegate        legacySourceProvider
	events          *legacyTestEvents
	removeErr       error
	failOnCancelled bool

	removeCalls  int
	removeDigest string
	removeCtx    context.Context
}

func (provider *legacyTestRecordingProvider) Probe(
	ctx context.Context,
	request legacyProbeRequest,
) (legacyProbeResult, error) {
	return provider.delegate.Probe(ctx, request)
}

func (provider *legacyTestRecordingProvider) Load(
	ctx context.Context,
	token legacyReadToken,
) ([]byte, error) {
	return provider.delegate.Load(ctx, token)
}

func (provider *legacyTestRecordingProvider) RemoveCommittedMarker(
	ctx context.Context,
	token legacyReadToken,
	digest string,
) error {
	provider.removeCalls++
	provider.removeDigest = digest
	provider.removeCtx = ctx
	if provider.events != nil {
		provider.events.add("remove-marker")
	}
	if provider.failOnCancelled && ctx.Err() != nil {
		return ctx.Err()
	}
	if provider.removeErr != nil {
		return provider.removeErr
	}
	return provider.delegate.RemoveCommittedMarker(ctx, token, digest)
}

type legacyTestEvents struct {
	mutex  sync.Mutex
	values []string
}

func (events *legacyTestEvents) add(value string) {
	if events == nil {
		return
	}
	events.mutex.Lock()
	defer events.mutex.Unlock()
	events.values = append(events.values, value)
}

func (events *legacyTestEvents) snapshot() []string {
	if events == nil {
		return nil
	}
	events.mutex.Lock()
	defer events.mutex.Unlock()
	return append([]string(nil), events.values...)
}

type legacyTestFilesystem struct {
	mutex sync.Mutex

	root                     string
	events                   *legacyTestEvents
	currentRenamed           bool
	failRootSyncAfterCurrent bool
	afterRootSync            func()
}

func (filesystem *legacyTestFilesystem) Sync(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	filesystem.mutex.Lock()
	isPostCurrentRoot := filesystem.currentRenamed &&
		info.IsDir() &&
		filepath.Clean(file.Name()) == filepath.Clean(filesystem.root)
	fail := isPostCurrentRoot && filesystem.failRootSyncAfterCurrent
	after := filesystem.afterRootSync
	filesystem.mutex.Unlock()

	if fail {
		if filesystem.events != nil {
			filesystem.events.add("root-sync-after-current-failed")
		}
		return errors.New("injected root sync failure")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if isPostCurrentRoot {
		if filesystem.events != nil {
			filesystem.events.add("root-sync-after-current")
		}
		if after != nil {
			after()
		}
	}
	return nil
}

func (filesystem *legacyTestFilesystem) RenameNoReplace(
	oldDirectory *os.File,
	oldName string,
	newDirectory *os.File,
	newName string,
) error {
	return unix.Renameat2(
		int(oldDirectory.Fd()),
		oldName,
		int(newDirectory.Fd()),
		newName,
		unix.RENAME_NOREPLACE,
	)
}

func (filesystem *legacyTestFilesystem) RenameReplace(
	oldDirectory *os.File,
	oldName string,
	newDirectory *os.File,
	newName string,
) error {
	if err := unix.Renameat(
		int(oldDirectory.Fd()),
		oldName,
		int(newDirectory.Fd()),
		newName,
	); err != nil {
		return err
	}
	if newName == "current" {
		filesystem.mutex.Lock()
		filesystem.currentRenamed = true
		filesystem.mutex.Unlock()
		if filesystem.events != nil {
			filesystem.events.add("rename-current")
		}
	}
	return nil
}

func legacyTestCandidate(
	parent string,
	sourceDigest string,
	consumed string,
	receipt *legacyReadToken,
	tag string,
) generationCandidate {
	object := []byte(
		`{"outbounds":[{"tag":` +
			fmtQuote(tag) +
			`,"type":"direct"}]}`,
	)
	return generationCandidate{
		ParentGenerationID:      parent,
		ConfigDigest:            strings.Repeat("c", 64),
		State:                   generationStateFresh,
		Objects:                 [][]byte{object},
		LegacyConsumedURLDigest: consumed,
		legacyMarkerReceipt:     receipt,
		Sources: []generationCandidateSource{{
			Index:       1,
			ObjectIndex: 1,
			URLDigest:   sourceDigest,
			Result:      sourceResultFresh,
			FetchCode:   fetchCodeOK,
			Info: NormalizeInfo{
				Format: FormatSingBoxJSON, Accepted: 1,
			},
		}},
	}
}

func legacyTestCommit(
	store generationStore,
	candidate generationCandidate,
) (generationCommitResult, error, *currentCommitGate) {
	ctx, gate := withNewCurrentCommitGate(context.Background())
	result, err := store.Commit(ctx, candidate)
	return result, err, gate
}

func legacyTestRandom(values ...byte) io.Reader {
	var random []byte
	for _, value := range values {
		random = append(random, bytes.Repeat([]byte{value}, 32)...)
	}
	return bytes.NewReader(random)
}

func legacyTestRequireMarker(
	t *testing.T,
	root string,
	digest string,
) {
	t.Helper()
	got, err := os.ReadFile(legacyTestMarkerPath(root))
	if err != nil || string(got) != digest+"\n" {
		t.Fatalf("marker=%q err=%v", got, err)
	}
}

type legacyTestFileIdentity struct {
	Device uint64
	Inode  uint64
}

func legacyTestIdentity(path string) (legacyTestFileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return legacyTestFileIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return legacyTestFileIdentity{}, errors.New("unexpected stat type")
	}
	return legacyTestFileIdentity{
		Device: uint64(stat.Dev),
		Inode:  stat.Ino,
	}, nil
}

func legacyTestOrdered(values []string, wanted ...string) bool {
	next := 0
	for _, value := range values {
		if next < len(wanted) && value == wanted[next] {
			next++
		}
	}
	return next == len(wanted)
}

func fmtQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
