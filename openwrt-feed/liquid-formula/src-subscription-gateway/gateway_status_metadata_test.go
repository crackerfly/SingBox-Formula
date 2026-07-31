package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func statusTestDegradedFixture(t *testing.T) *stateTestFixture {
	t.Helper()
	fixture := newStateTestFixture(t, 2)
	fixture.Manifest.ConfigDigest = validGatewayDigest
	fixture.Manifest.Sources[1].URLSHA256 = stateTestSHA256(
		[]byte("https://second.invalid/subscription"),
	)
	fixture.Status.State = generationStateDegraded
	fixture.Status.FreshCount = 1
	fixture.Status.FallbackIndices = []int{2}
	fixture.Status.Sources[1].Result = sourceResultFallback
	fixture.Status.Sources[1].FetchCode = string(fetchCodeTimeout)
	fixture.Status.Sources[1].Format = string(FormatClashYAML)
	fixture.Status.Sources[1].Skipped = 1
	fixture.Status.Sources[1].Warnings = []Warning{{
		Code:      "invalid_field",
		NodeIndex: 3,
		Type:      "vmess",
		Field:     "port",
	}}
	fixture.writeStatus(t)
	fixture.writeManifest(t)
	return fixture
}

func statusTestConfig(fixture *stateTestFixture) gatewayConfig {
	return gatewayConfig{
		ConfigDigest:            validGatewayDigest,
		AggregateTimeoutSeconds: 5,
		Sources: []gatewaySource{
			{URL: "https://first.invalid/private", URLDigest: strings.Repeat("1", 64)},
			{URL: "https://second.invalid/private", URLDigest: strings.Repeat("2", 64)},
		},
	}
}

func TestSafeStatusReadsOnlyBoundedGenerationMetadata(t *testing.T) {
	fixture := statusTestDegradedFixture(t)

	// Keep the exact lengths and secure modes but poison both potentially large
	// payloads. A polling status read must not parse or hash either payload.
	poisonedObject := bytes.Repeat([]byte("x"), len(fixture.Object))
	poisonedAggregate := bytes.Repeat([]byte("y"), len(fixture.Aggregate))
	stateTestWriteFile(t, fixture.ObjectPath, poisonedObject)
	stateTestWriteFile(t, fixture.AggregatePath, poisonedAggregate)

	status := readSafeSubscriptionStatus(
		fixture.Root, validGatewayDigest, 2,
	)
	if status.OverallState != generationStateDegraded ||
		!status.ConfigMatch ||
		status.ActiveGeneration != fixture.GenerationID ||
		status.TotalSources != 2 || status.FreshCount != 1 ||
		len(status.FallbackIndices) != 1 || status.FallbackIndices[0] != 2 ||
		len(status.Sources) != 2 {
		t.Fatalf("safe status = %#v", status)
	}
	second := status.Sources[1]
	if second.Index != 2 || second.Result != sourceResultFallback ||
		second.FetchCode != string(fetchCodeTimeout) ||
		second.Format != string(FormatClashYAML) ||
		second.Accepted != 1 || second.Skipped != 1 ||
		len(second.Warnings) != 1 || second.Warnings[0].Code != "invalid_field" {
		t.Fatalf("second source = %#v", second)
	}
}

func TestSafeStatusNeverReturnsStoredSecretsOrInternalDigests(t *testing.T) {
	fixture := statusTestDegradedFixture(t)
	status := readSafeSubscriptionStatus(
		fixture.Root, validGatewayDigest, 2,
	)
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"https://", "first.invalid", "second.invalid", configSecretCanary,
		validGatewayDigest, fixture.URLDigest, fixture.ObjectDigest,
		"password", "token", "uuid", "private",
	} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("safe status exposes %q: %s", forbidden, raw)
		}
	}
	if !bytes.Contains(raw, []byte(fixture.GenerationID)) {
		t.Fatalf("safe status omits active generation: %s", raw)
	}
}

func TestFailedAttemptPreservesSelectedGenerationAndOverridesOverallState(t *testing.T) {
	fixture := statusTestDegradedFixture(t)
	statusBefore, err := os.ReadFile(fixture.StatusPath)
	if err != nil {
		t.Fatal(err)
	}
	currentBefore, err := os.ReadFile(fixture.CurrentPath)
	if err != nil {
		t.Fatal(err)
	}

	recorder := newDiskLastAttemptRecorder(fixture.Root)
	config := statusTestConfig(fixture)
	if err := recorder.RecordFailure(config, aggregateOutcome{
		Code:             aggregateCodeSourceUnavailable,
		SourceIndex:      2,
		Preserved:        true,
		FailureStage:     failureStageSourceFetch,
		FetchCode:        fetchCodeHTTPStatus,
		ActiveGeneration: fixture.GenerationID,
		CurrentObserved:  true,
	}); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	statusAfter, _ := os.ReadFile(fixture.StatusPath)
	currentAfter, _ := os.ReadFile(fixture.CurrentPath)
	if !bytes.Equal(statusBefore, statusAfter) ||
		!bytes.Equal(currentBefore, currentAfter) {
		t.Fatal("failure mutated the selected immutable generation")
	}

	attemptPath := filepath.Join(fixture.Root, "last-attempt.json")
	info, err := os.Stat(attemptPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > 4096 {
		t.Fatalf("last attempt mode/size = %o/%d", info.Mode().Perm(), info.Size())
	}
	document := stateTestReadJSONMap(t, attemptPath)
	wantKeys := []string{
		"schema", "config_digest", "active_generation", "state",
		"total_sources", "failure_stage", "code", "fetch_code",
		"source_index", "preserved",
	}
	if !diskExactObjectKeys(document, wantKeys) {
		t.Fatalf("last-attempt keys = %#v", document)
	}

	status := readSafeSubscriptionStatus(
		fixture.Root, validGatewayDigest, 2,
	)
	if status.OverallState != subscriptionOverallFailed ||
		!status.ConfigMatch || status.ActiveGeneration != fixture.GenerationID ||
		status.FreshCount != 1 || len(status.Sources) != 2 ||
		status.LastAttempt == nil ||
		status.LastAttempt.FailureStage != string(failureStageSourceFetch) ||
		status.LastAttempt.Code != string(aggregateCodeSourceUnavailable) ||
		status.LastAttempt.FetchCode != string(fetchCodeHTTPStatus) ||
		status.LastAttempt.SourceIndex != 2 || !status.LastAttempt.Preserved {
		t.Fatalf("failed safe status = %#v", status)
	}
}

func TestBusyAggregateNeverOverwritesLastAttempt(t *testing.T) {
	fixture := statusTestDegradedFixture(t)
	recorder := newDiskLastAttemptRecorder(fixture.Root)
	config := statusTestConfig(fixture)
	if err := recorder.RecordFailure(config, aggregateOutcome{
		Code:             aggregateCodeSourceUnavailable,
		SourceIndex:      1,
		Preserved:        true,
		FailureStage:     failureStageSourceNormalize,
		FetchCode:        fetchCodeNormalize,
		ActiveGeneration: fixture.GenerationID,
		CurrentObserved:  true,
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.Root, "last-attempt.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	engine := &attemptRecordingEngine{
		aggregateEngine: staticAggregateEngine{outcome: aggregateOutcome{
			Code: aggregateCodeBusy, Preserved: true,
		}},
		recorder: recorder,
	}
	handler := gatewayHTTPHandler{config: config, engine: engine}
	recorderHTTP := httptest.NewRecorder()
	handler.ServeHTTP(
		recorderHTTP,
		httptest.NewRequest(http.MethodGet, "/v1/aggregate", nil),
	)
	if recorderHTTP.Code != http.StatusServiceUnavailable {
		t.Fatalf("busy status = %d", recorderHTTP.Code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("busy aggregate overwrote the last complete attempt")
	}
}

func TestLastAttemptRenameFaultPreservesPreviousCompleteRecord(t *testing.T) {
	fixture := statusTestDegradedFixture(t)
	config := statusTestConfig(fixture)
	recorder := newDiskLastAttemptRecorder(fixture.Root)
	if err := recorder.RecordFailure(config, aggregateOutcome{
		Code:             aggregateCodeSourceUnavailable,
		SourceIndex:      1,
		Preserved:        true,
		FailureStage:     failureStageSourceFetch,
		FetchCode:        fetchCodeTimeout,
		ActiveGeneration: fixture.GenerationID,
		CurrentObserved:  true,
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.Root, "last-attempt.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	faulted := newDiskLastAttemptRecorder(fixture.Root)
	faulted.faultHook = func(stage string) error {
		if stage == "before_last_attempt_rename" {
			return errors.New("injected")
		}
		return nil
	}
	if err := faulted.RecordFailure(config, aggregateOutcome{
		Code:             aggregateCodeCommitFailed,
		Preserved:        true,
		FailureStage:     failureStageCommit,
		ActiveGeneration: fixture.GenerationID,
		CurrentObserved:  true,
	}); err == nil {
		t.Fatal("rename fault unexpectedly succeeded")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rename fault replaced the previous complete attempt")
	}
	matches, err := filepath.Glob(filepath.Join(fixture.Root, ".last-attempt-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary last-attempt files = %#v, err=%v", matches, err)
	}
}

func TestFailureRecordUsesTransactionBoundGenerationAcrossPointerRace(t *testing.T) {
	fixture := statusTestDegradedFixture(t)
	newerGeneration := strings.Repeat("c", 64)
	stateTestWriteFile(
		t, fixture.CurrentPath, []byte(newerGeneration+"\n"),
	)
	recorder := newDiskLastAttemptRecorder(fixture.Root)
	if err := recorder.RecordFailure(
		statusTestConfig(fixture),
		aggregateOutcome{
			Code:             aggregateCodeSourceUnavailable,
			SourceIndex:      2,
			Preserved:        true,
			FailureStage:     failureStageSourceFetch,
			FetchCode:        fetchCodeTimeout,
			ActiveGeneration: fixture.GenerationID,
			CurrentObserved:  true,
		},
	); err != nil {
		t.Fatalf("record raced failure: %v", err)
	}
	document := stateTestReadJSONMap(
		t, filepath.Join(fixture.Root, "last-attempt.json"),
	)
	if document["active_generation"] != fixture.GenerationID {
		t.Fatalf(
			"record attached to %v, want transaction generation %s",
			document["active_generation"], fixture.GenerationID,
		)
	}
}

func TestFailureObservedAbsentMustNotBindLaterSuccess(t *testing.T) {
	fixture := newStateTestFixture(t, 1)
	fixture.Manifest.ConfigDigest = validGatewayDigest
	fixture.writeStatus(t)
	fixture.writeManifest(t)

	config := gatewayConfig{
		ConfigDigest: validGatewayDigest,
		Sources: []gatewaySource{{
			URL:       "https://one.invalid",
			URLDigest: fixture.URLDigest,
		}},
	}
	// This outcome belongs to a transaction which observed current as absent.
	// The fixture models another request publishing its first generation after
	// that transaction returned but before the HTTP handler records the failure.
	outcome := aggregateOutcome{
		Code:             aggregateCodeSourceUnavailable,
		SourceIndex:      1,
		FailureStage:     failureStageSourceFetch,
		FetchCode:        fetchCodeTimeout,
		ActiveGeneration: "",
		Preserved:        false,
		CurrentObserved:  true,
	}
	if err := newDiskLastAttemptRecorder(fixture.Root).
		RecordFailure(config, outcome); err != nil {
		t.Fatal(err)
	}
	document := stateTestReadJSONMap(
		t, filepath.Join(fixture.Root, "last-attempt.json"),
	)
	if document["active_generation"] != "" {
		t.Fatalf(
			"failure observed no generation but was rebound to later current %v",
			document["active_generation"],
		)
	}
}

func TestFailureRecorderRejectsAnUnobservedCurrent(t *testing.T) {
	fixture := statusTestDegradedFixture(t)
	attemptPath := filepath.Join(fixture.Root, "last-attempt.json")
	err := newDiskLastAttemptRecorder(fixture.Root).RecordFailure(
		statusTestConfig(fixture),
		aggregateOutcome{
			Code:         aggregateCodeSourceUnavailable,
			SourceIndex:  1,
			FailureStage: failureStageSourceFetch,
			FetchCode:    fetchCodeTimeout,
		},
	)
	if err == nil {
		t.Fatal("unbound failure record unexpectedly succeeded")
	}
	if _, statErr := os.Lstat(attemptPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unbound failure published metadata: %v", statErr)
	}
}

func TestStatusCLIEmitsBoundedSafeJSONWithoutPayloadData(t *testing.T) {
	fixture := statusTestDegradedFixture(t)
	var stdout, stderr bytes.Buffer
	code := runStatus(
		[]string{
			"--config", "/etc/liquid-formula/config.yaml",
			"--expected-digest", validGatewayDigest,
		},
		&stdout,
		&stderr,
		statusDependencies{
			ReadFile: func(path string) ([]byte, error) {
				if path != "/etc/liquid-formula/config.yaml" {
					t.Fatalf("config path = %q", path)
				}
				return []byte(validGatewayYAML), nil
			},
			StateRoot: fixture.Root,
		},
	)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("status CLI = %d, stderr=%q", code, stderr.String())
	}
	if stdout.Len() == 0 || stdout.Len() > 32768 {
		t.Fatalf("status output bytes = %d", stdout.Len())
	}
	var status safeSubscriptionStatus
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		t.Fatalf("decode status CLI: %v", err)
	}
	if status.OverallState != generationStateDegraded ||
		status.TotalSources != 2 || len(status.Sources) != 2 {
		t.Fatalf("CLI status = %#v", status)
	}
	for _, forbidden := range []string{
		configSecretCanary, "https://", validGatewayDigest,
		fixture.URLDigest, fixture.ObjectDigest,
	} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("CLI status exposes %q", forbidden)
		}
	}
}

func TestSafeStatusReturnsEmptyAndUnavailableWithoutLeakingState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	empty := readSafeSubscriptionStatus(root, validGatewayDigest, 2)
	if empty.OverallState != subscriptionOverallEmpty ||
		empty.ConfigMatch || empty.TotalSources != 2 ||
		len(empty.Sources) != 0 || empty.LastAttempt != nil {
		t.Fatalf("empty status = %#v", empty)
	}

	fixture := statusTestDegradedFixture(t)
	stateTestWriteFile(
		t, filepath.Join(fixture.Root, "last-attempt.json"),
		[]byte(`{"schema":1,"secret":"https://private.invalid/token"}`),
	)
	unavailable := readSafeSubscriptionStatus(
		fixture.Root, validGatewayDigest, 2,
	)
	raw, err := json.Marshal(unavailable)
	if err != nil {
		t.Fatal(err)
	}
	if unavailable.OverallState != subscriptionOverallUnavailable ||
		bytes.Contains(raw, []byte("private.invalid")) ||
		bytes.Contains(raw, []byte("token")) {
		t.Fatalf("unavailable status = %s", raw)
	}
}

func TestSafeStatusNeverBlocksOnSpecialMetadataFiles(t *testing.T) {
	for _, relative := range []string{
		"current",
		filepath.Join("generations", "selected", "manifest.json"),
		filepath.Join("generations", "selected", "status.json"),
		"last-attempt.json",
	} {
		t.Run(relative, func(t *testing.T) {
			fixture := statusTestDegradedFixture(t)
			path := relative
			if strings.Contains(relative, "selected") {
				path = strings.Replace(
					relative, "selected", fixture.GenerationID, 1,
				)
			}
			path = filepath.Join(fixture.Root, path)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}

			finished := make(chan safeSubscriptionStatus, 1)
			go func() {
				finished <- readSafeSubscriptionStatus(
					fixture.Root, validGatewayDigest, 2,
				)
			}()
			select {
			case status := <-finished:
				if status.OverallState != subscriptionOverallUnavailable {
					t.Fatalf("special-file status = %#v", status)
				}
			case <-time.After(250 * time.Millisecond):
				// Unblock the old blocking-open implementation so the failed
				// test cannot leave a stuck goroutine behind.
				fd, err := unix.Open(
					path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0,
				)
				if err == nil {
					_ = unix.Close(fd)
				}
				select {
				case <-finished:
				case <-time.After(time.Second):
				}
				t.Fatal("safe status blocked while opening a special metadata file")
			}
		})
	}
}

func TestGenerationMetadataEnumerationIsCappedAtFourEntries(t *testing.T) {
	called := false
	ok := diskGenerationHasExactFilesFromReadDir(
		func(limit int) ([]os.DirEntry, error) {
			called = true
			if limit != 4 {
				t.Fatalf("ReadDir limit = %d, want 4", limit)
			}
			return nil, errors.New("injected enumeration failure")
		},
	)
	if !called || ok {
		t.Fatalf("bounded enumeration = called:%v ok:%v", called, ok)
	}
}

func TestMatchingAttemptMustAgreeWithCurrentConfigSourceTotal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	state, err := diskOpenOrCreateState(
		root, nativeDiskGenerationFilesystem{},
	)
	if err != nil {
		t.Fatal(err)
	}
	state.close()
	recorder := newDiskLastAttemptRecorder(root)
	config := gatewayConfig{
		ConfigDigest: validGatewayDigest,
		Sources: []gatewaySource{
			{URL: "https://one.invalid", URLDigest: strings.Repeat("1", 64)},
			{URL: "https://two.invalid", URLDigest: strings.Repeat("2", 64)},
		},
	}
	if err := recorder.RecordFailure(config, aggregateOutcome{
		Code:            aggregateCodeSourceUnavailable,
		SourceIndex:     2,
		FailureStage:    failureStageSourceFetch,
		FetchCode:       fetchCodeTimeout,
		CurrentObserved: true,
	}); err != nil {
		t.Fatal(err)
	}
	status := readSafeSubscriptionStatus(root, validGatewayDigest, 1)
	if status.OverallState != subscriptionOverallUnavailable ||
		status.LastAttempt != nil {
		t.Fatalf("mismatched attempt total accepted: %#v", status)
	}
}

func TestLastAttemptRejectsFailureStageAndCodeDisagreement(t *testing.T) {
	root := filepath.Join(t.TempDir(), "subscriptions")
	state, err := diskOpenOrCreateState(
		root, nativeDiskGenerationFilesystem{},
	)
	if err != nil {
		t.Fatal(err)
	}
	state.close()
	record := []byte(`{"schema":1,"config_digest":"` + validGatewayDigest +
		`","active_generation":"","state":"failed","total_sources":2,` +
		`"failure_stage":"source_fetch","code":"commit_failed",` +
		`"fetch_code":"","source_index":0,"preserved":false}`)
	stateTestWriteFile(t, filepath.Join(root, "last-attempt.json"), record)
	status := readSafeSubscriptionStatus(root, validGatewayDigest, 2)
	if status.OverallState != subscriptionOverallUnavailable ||
		status.LastAttempt != nil {
		t.Fatalf("inconsistent attempt accepted: %#v", status)
	}
}

func TestHTTPCompleteFailurePublishesLastAttempt(t *testing.T) {
	fixture := statusTestDegradedFixture(t)
	config := statusTestConfig(fixture)
	engine := &attemptRecordingEngine{
		aggregateEngine: staticAggregateEngine{outcome: aggregateOutcome{
			Code:             aggregateCodeSourceUnavailable,
			SourceIndex:      2,
			Preserved:        true,
			FailureStage:     failureStageSourceNormalize,
			FetchCode:        fetchCodeNormalize,
			ActiveGeneration: fixture.GenerationID,
			CurrentObserved:  true,
		}},
		recorder: newDiskLastAttemptRecorder(fixture.Root),
	}
	handler := gatewayHTTPHandler{config: config, engine: engine}
	response := httptest.NewRecorder()
	handler.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/v1/aggregate", nil),
	)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("failure response = %d: %s", response.Code, response.Body.String())
	}
	status := readSafeSubscriptionStatus(
		fixture.Root, validGatewayDigest, 2,
	)
	if status.OverallState != subscriptionOverallFailed ||
		status.LastAttempt == nil ||
		status.LastAttempt.FailureStage != string(failureStageSourceNormalize) ||
		status.LastAttempt.FetchCode != string(fetchCodeNormalize) {
		t.Fatalf("HTTP failure status = %#v", status)
	}
}

func statusTestWriteSecondGeneration(
	t *testing.T,
	fixture *stateTestFixture,
	generation string,
) {
	t.Helper()
	directory := filepath.Join(fixture.GenerationsDir, generation)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	status := fixture.Status
	status.Generation = generation
	status.State = generationStateDegraded
	status.FreshCount = 0
	status.FallbackIndices = []int{1}
	status.Sources = append([]stateTestStatusSource(nil), fixture.Status.Sources...)
	status.Sources[0].Result = sourceResultFallback
	status.Sources[0].FetchCode = string(fetchCodeTimeout)
	statusRaw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	manifest := fixture.Manifest
	manifest.Generation = generation
	manifest.Parent = fixture.GenerationID
	manifest.StatusSHA256 = stateTestSHA256(statusRaw)
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	stateTestWriteFile(
		t, filepath.Join(directory, "aggregate.json"), fixture.Aggregate,
	)
	stateTestWriteFile(
		t, filepath.Join(directory, "manifest.json"), manifestRaw,
	)
	stateTestWriteFile(
		t, filepath.Join(directory, "status.json"), statusRaw,
	)
}

func TestStatusRetriesWhenCurrentChangesDuringMetadataRead(t *testing.T) {
	fixture := newStateTestFixture(t, 1)
	fixture.Manifest.ConfigDigest = validGatewayDigest
	fixture.writeStatus(t)
	fixture.writeManifest(t)
	newer := strings.Repeat("c", 64)
	statusTestWriteSecondGeneration(t, fixture, newer)
	changed := false
	status := readSafeSubscriptionStatusWithHook(
		fixture.Root, validGatewayDigest, 1,
		func(stage string) {
			if stage != "after_selected_metadata" || changed {
				return
			}
			changed = true
			stateTestWriteFile(
				t, fixture.CurrentPath, []byte(newer+"\n"),
			)
		},
	)
	if !changed || status.OverallState != generationStateDegraded ||
		status.ActiveGeneration != newer || status.FreshCount != 0 ||
		len(status.FallbackIndices) != 1 || status.FallbackIndices[0] != 1 {
		t.Fatalf("raced safe status = %#v, changed=%v", status, changed)
	}
}

func TestConcurrentAttemptWritersAndStatusReadersNeverExposePartialJSON(t *testing.T) {
	fixture := statusTestDegradedFixture(t)
	config := statusTestConfig(fixture)
	const iterations = 24
	var wait sync.WaitGroup
	errorsFound := make(chan string, iterations*2)
	for worker := 0; worker < 2; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			recorder := newDiskLastAttemptRecorder(fixture.Root)
			for index := 0; index < iterations; index++ {
				outcome := aggregateOutcome{
					Code:             aggregateCodeSourceUnavailable,
					SourceIndex:      worker + 1,
					Preserved:        true,
					FailureStage:     failureStageSourceFetch,
					FetchCode:        fetchCodeTimeout,
					ActiveGeneration: fixture.GenerationID,
					CurrentObserved:  true,
				}
				if err := recorder.RecordFailure(config, outcome); err != nil {
					errorsFound <- err.Error()
					return
				}
			}
		}(worker)
	}
	for worker := 0; worker < 2; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < iterations; index++ {
				status := readSafeSubscriptionStatus(
					fixture.Root, validGatewayDigest, 2,
				)
				if status.OverallState != generationStateDegraded &&
					status.OverallState != subscriptionOverallFailed {
					errorsFound <- status.OverallState
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for failure := range errorsFound {
		t.Fatalf("concurrent status failure: %s", failure)
	}
}
