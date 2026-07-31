package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const stateSchemaLimit = 64 * 1024

type stateTestAggregateMetadata struct {
	SHA256    string `json:"sha256"`
	Bytes     int    `json:"bytes"`
	Outbounds int    `json:"outbounds"`
}

type stateTestManifestSource struct {
	Index        int    `json:"index"`
	URLSHA256    string `json:"url_sha256"`
	ObjectSHA256 string `json:"object_sha256"`
	Bytes        int    `json:"bytes"`
	Outbounds    int    `json:"outbounds"`
}

type stateTestManifest struct {
	Schema                  int                        `json:"schema"`
	Generation              string                     `json:"generation"`
	Parent                  string                     `json:"parent"`
	ConfigDigest            string                     `json:"config_digest"`
	Aggregate               stateTestAggregateMetadata `json:"aggregate"`
	StatusSHA256            string                     `json:"status_sha256"`
	Sources                 []stateTestManifestSource  `json:"sources"`
	LegacyConsumedURLSHA256 string                     `json:"legacy_consumed_url_sha256"`
}

type stateTestStatusSource struct {
	Index     int       `json:"index"`
	Result    string    `json:"result"`
	FetchCode string    `json:"fetch_code"`
	Format    string    `json:"format"`
	Accepted  int       `json:"accepted"`
	Skipped   int       `json:"skipped"`
	Warnings  []Warning `json:"warnings"`
}

type stateTestStatus struct {
	Schema          int                     `json:"schema"`
	Generation      string                  `json:"generation"`
	State           string                  `json:"state"`
	FreshCount      int                     `json:"fresh_count"`
	FallbackIndices []int                   `json:"fallback_indices"`
	Sources         []stateTestStatusSource `json:"sources"`
}

type stateTestFixture struct {
	Root           string
	GenerationsDir string
	ObjectsDir     string
	GenerationDir  string
	CurrentPath    string
	AggregatePath  string
	ManifestPath   string
	StatusPath     string
	ObjectPath     string

	GenerationID string
	ConfigDigest string
	URLDigest    string
	ObjectDigest string
	Aggregate    []byte
	Object       []byte
	Manifest     stateTestManifest
	Status       stateTestStatus
}

func stateTestSHA256(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func stateTestWriteFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func newStateTestFixture(t *testing.T, occurrences int) *stateTestFixture {
	t.Helper()
	if occurrences < 1 {
		t.Fatalf("occurrences = %d, want at least one", occurrences)
	}

	root := filepath.Join(t.TempDir(), "subscriptions")
	generationsDir := filepath.Join(root, "generations")
	objectsDir := filepath.Join(root, "objects")
	generationID := strings.Repeat("a", 64)
	generationDir := filepath.Join(generationsDir, generationID)
	for _, directory := range []string{
		root, generationsDir, objectsDir, generationDir,
	} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatalf("mkdir %s: %v", directory, err)
		}
		if err := os.Chmod(directory, 0700); err != nil {
			t.Fatalf("chmod %s: %v", directory, err)
		}
	}

	object := []byte(
		`{"outbounds":[{"tag":"Fixture","type":"direct"}]}`,
	)
	aggregate := append([]byte(nil), object...)
	objectDigest := stateTestSHA256(object)
	configDigest := strings.Repeat("b", 64)
	urlDigest := stateTestSHA256(
		[]byte("https://fixture.invalid/subscription"),
	)

	fixture := &stateTestFixture{
		Root:           root,
		GenerationsDir: generationsDir,
		ObjectsDir:     objectsDir,
		GenerationDir:  generationDir,
		CurrentPath:    filepath.Join(root, "current"),
		AggregatePath:  filepath.Join(generationDir, "aggregate.json"),
		ManifestPath:   filepath.Join(generationDir, "manifest.json"),
		StatusPath:     filepath.Join(generationDir, "status.json"),
		ObjectPath:     filepath.Join(objectsDir, objectDigest+".json"),
		GenerationID:   generationID,
		ConfigDigest:   configDigest,
		URLDigest:      urlDigest,
		ObjectDigest:   objectDigest,
		Aggregate:      aggregate,
		Object:         object,
	}

	fixture.Manifest = stateTestManifest{
		Schema:       1,
		Generation:   generationID,
		Parent:       "",
		ConfigDigest: configDigest,
		Aggregate: stateTestAggregateMetadata{
			SHA256:    stateTestSHA256(aggregate),
			Bytes:     len(aggregate),
			Outbounds: 1,
		},
		Sources:                 make([]stateTestManifestSource, occurrences),
		LegacyConsumedURLSHA256: "",
	}
	fixture.Status = stateTestStatus{
		Schema:          1,
		Generation:      generationID,
		State:           generationStateFresh,
		FreshCount:      occurrences,
		FallbackIndices: []int{},
		Sources:         make([]stateTestStatusSource, occurrences),
	}
	for index := 0; index < occurrences; index++ {
		fixture.Manifest.Sources[index] = stateTestManifestSource{
			Index:        index + 1,
			URLSHA256:    urlDigest,
			ObjectSHA256: objectDigest,
			Bytes:        len(object),
			Outbounds:    1,
		}
		fixture.Status.Sources[index] = stateTestStatusSource{
			Index:     index + 1,
			Result:    sourceResultFresh,
			FetchCode: string(fetchCodeOK),
			Format:    string(FormatSingBoxJSON),
			Accepted:  1,
			Skipped:   0,
			Warnings:  []Warning{},
		}
	}

	stateTestWriteFile(t, fixture.ObjectPath, object)
	stateTestWriteFile(t, fixture.AggregatePath, aggregate)
	fixture.writeStatus(t)
	fixture.writeManifest(t)
	stateTestWriteFile(
		t, fixture.CurrentPath, []byte(generationID+"\n"),
	)
	return fixture
}

func (fixture *stateTestFixture) writeManifest(t *testing.T) {
	t.Helper()
	contents, err := json.Marshal(fixture.Manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	stateTestWriteFile(t, fixture.ManifestPath, contents)
}

func (fixture *stateTestFixture) writeStatus(t *testing.T) {
	t.Helper()
	contents, err := json.Marshal(fixture.Status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	stateTestWriteFile(t, fixture.StatusPath, contents)
	fixture.Manifest.StatusSHA256 = stateTestSHA256(contents)
}

func stateTestStore(root string) generationStore {
	return newDiskGenerationStore(root)
}

func requireValidState(
	t *testing.T,
	fixture *stateTestFixture,
) currentSelection {
	t.Helper()
	store := stateTestStore(fixture.Root)
	observation, err := store.ObserveCurrent(context.Background())
	if err != nil {
		t.Fatalf("observe valid state: %v", err)
	}
	if observation != (currentObservation{
		Kind:         currentPresent,
		GenerationID: fixture.GenerationID,
		Validated:    true,
	}) {
		t.Fatalf("valid observation = %#v", observation)
	}
	selection, err := store.LoadCurrent(context.Background())
	if err != nil {
		t.Fatalf("load valid state: %v", err)
	}
	if selection.Kind != currentPresent {
		t.Fatalf("valid selection kind = %d, want present", selection.Kind)
	}
	return selection
}

func requireRejectedState(t *testing.T, root string) {
	t.Helper()
	store := stateTestStore(root)
	observation, _ := store.ObserveCurrent(context.Background())
	if observation.Validated {
		t.Fatalf("invalid state was reported as validated: %#v", observation)
	}
	selection, err := store.LoadCurrent(context.Background())
	if err == nil && selection.Kind == currentPresent {
		t.Fatalf("invalid state loaded as present: %#v", selection)
	}
}

func TestDiskGenerationStoreLoadsTemporaryValidTree(t *testing.T) {
	fixture := newStateTestFixture(t, 1)
	selection := requireValidState(t, fixture)
	generation := selection.Generation
	if generation.GenerationID != fixture.GenerationID ||
		generation.ConfigDigest != fixture.ConfigDigest ||
		!bytes.Equal(generation.Aggregate, fixture.Aggregate) {
		t.Fatalf("loaded generation = %#v", generation)
	}
	if len(generation.Sources) != 1 {
		t.Fatalf("loaded source count = %d, want 1", len(generation.Sources))
	}
	source := generation.Sources[0]
	if source.Index != 1 ||
		source.URLDigest != fixture.URLDigest ||
		source.ObjectDigest != fixture.ObjectDigest ||
		!bytes.Equal(source.Normalized, fixture.Object) ||
		source.Info.Format != FormatSingBoxJSON ||
		source.Info.Accepted != 1 ||
		source.Info.Skipped != 0 ||
		len(source.Info.Warnings) != 0 {
		t.Fatalf("loaded source = %#v", source)
	}
}

func TestDiskGenerationStoreTreatsMissingCurrentAsAbsent(t *testing.T) {
	fixture := newStateTestFixture(t, 1)
	if err := os.Remove(fixture.CurrentPath); err != nil {
		t.Fatal(err)
	}
	store := stateTestStore(fixture.Root)
	observation, err := store.ObserveCurrent(context.Background())
	if err != nil {
		t.Fatalf("observe absent current: %v", err)
	}
	if observation != (currentObservation{Kind: currentAbsent}) {
		t.Fatalf("absent observation = %#v", observation)
	}
	selection, err := store.LoadCurrent(context.Background())
	if err != nil {
		t.Fatalf("load absent current: %v", err)
	}
	if selection.Kind != currentAbsent {
		t.Fatalf("absent selection = %#v", selection)
	}
}

func TestDiskGenerationStoreRequiresExactCurrentSyntax(t *testing.T) {
	validID := strings.Repeat("a", 64)
	for _, testCase := range []struct {
		name     string
		contents []byte
	}{
		{name: "empty", contents: []byte{}},
		{name: "missing newline", contents: []byte(validID)},
		{name: "extra newline", contents: []byte(validID + "\n\n")},
		{name: "uppercase", contents: []byte(strings.Repeat("A", 64) + "\n")},
		{name: "short", contents: []byte(strings.Repeat("a", 63) + "\n")},
		{name: "long", contents: []byte(strings.Repeat("a", 65) + "\n")},
		{name: "non hex", contents: []byte(strings.Repeat("g", 64) + "\n")},
		{
			name: "embedded nul",
			contents: append(
				append([]byte(nil), []byte(validID[:32])...),
				append([]byte{0}, []byte(validID[33:]+"\n")...)...,
			),
		},
		{name: "trailing space", contents: []byte(validID + " \n")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			stateTestWriteFile(t, fixture.CurrentPath, testCase.contents)
			requireRejectedState(t, fixture.Root)
		})
	}
}

func TestStateFixtureUsesOnlyLowercaseDigestDerivedPaths(t *testing.T) {
	fixture := newStateTestFixture(t, 1)
	for name, value := range map[string]string{
		"generation": fixture.GenerationID,
		"config":     fixture.ConfigDigest,
		"url":        fixture.URLDigest,
		"object":     fixture.ObjectDigest,
	} {
		if len(value) != 64 {
			t.Fatalf("%s digest length = %d", name, len(value))
		}
		if _, err := hex.DecodeString(value); err != nil ||
			value != strings.ToLower(value) {
			t.Fatalf("%s digest is not lowercase hex: %q", name, value)
		}
	}
	if got := filepath.Base(fixture.GenerationDir); got != fixture.GenerationID {
		t.Fatalf("generation path base = %q", got)
	}
	if got := filepath.Base(fixture.ObjectPath); got !=
		fmt.Sprintf("%s.json", fixture.ObjectDigest) {
		t.Fatalf("object path base = %q", got)
	}
}
