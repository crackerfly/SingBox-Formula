package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (seed *commitTestGCSeed) commitFourthNormally(
	t *testing.T,
) generationCommitResult {
	t.Helper()
	parent := seed.generations[len(seed.generations)-1]
	result, err, _ := commitTestCommit(
		t,
		seed.store,
		commitTestCandidate(parent, seed.objects[3]),
	)
	selection := commitTestRequireCommitted(t, result, err)
	seed.generations = append(
		seed.generations, selection.Generation.GenerationID,
	)
	return result
}

func TestDiskGCPreservesSuspiciousTopLevelEntriesWhileCleaningSafeOrphans(
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

	externalGeneration := filepath.Join(t.TempDir(), "outside-generation")
	if err := os.Mkdir(externalGeneration, 0700); err != nil {
		t.Fatal(err)
	}
	externalGenerationSentinel := filepath.Join(
		externalGeneration, "DO_NOT_FOLLOW",
	)
	if err := os.WriteFile(
		externalGenerationSentinel, []byte("generation sentinel"), 0600,
	); err != nil {
		t.Fatal(err)
	}
	suspiciousGenerationLink := filepath.Join(
		seed.root, "generations", strings.Repeat("f", 64),
	)
	if err := os.Symlink(
		externalGeneration, suspiciousGenerationLink,
	); err != nil {
		t.Fatal(err)
	}
	nonstandardGenerationFile := filepath.Join(
		seed.root, "generations", "README",
	)
	if err := os.WriteFile(
		nonstandardGenerationFile, []byte("leave me"), 0600,
	); err != nil {
		t.Fatal(err)
	}
	nonstandardGenerationDirectory := filepath.Join(
		seed.root, "generations", "not-a-generation",
	)
	if err := os.Mkdir(nonstandardGenerationDirectory, 0700); err != nil {
		t.Fatal(err)
	}

	externalObject := filepath.Join(t.TempDir(), "outside-object.json")
	externalObjectContents := []byte("object sentinel")
	if err := os.WriteFile(
		externalObject, externalObjectContents, 0600,
	); err != nil {
		t.Fatal(err)
	}
	suspiciousObjectLink := filepath.Join(
		seed.root, "objects", strings.Repeat("e", 64)+".json",
	)
	if err := os.Symlink(externalObject, suspiciousObjectLink); err != nil {
		t.Fatal(err)
	}
	nonstandardObjectFile := filepath.Join(
		seed.root, "objects", "README",
	)
	if err := os.WriteFile(
		nonstandardObjectFile, []byte("leave me"), 0600,
	); err != nil {
		t.Fatal(err)
	}
	nonstandardObjectDirectory := filepath.Join(
		seed.root, "objects", "not-an-object",
	)
	if err := os.Mkdir(nonstandardObjectDirectory, 0700); err != nil {
		t.Fatal(err)
	}

	seed.commitFourthNormally(t)

	commitTestRequireAbsent(t, oldGeneration)
	commitTestRequireAbsent(t, oldObject)
	for _, path := range []string{
		suspiciousGenerationLink,
		nonstandardGenerationFile,
		nonstandardGenerationDirectory,
		suspiciousObjectLink,
		nonstandardObjectFile,
		nonstandardObjectDirectory,
	} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("suspicious entry %s changed: %v", path, err)
		}
	}
	if got, err := os.ReadFile(externalGenerationSentinel); err != nil ||
		string(got) != "generation sentinel" {
		t.Fatalf("generation symlink target changed: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(externalObject); err != nil ||
		!bytes.Equal(got, externalObjectContents) {
		t.Fatalf("object symlink target changed: %q err=%v", got, err)
	}
}

func TestDiskGCRefusesUnsafeGenerationDeletion(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string) string
	}{
		{
			name: "extra entry",
			mutate: func(t *testing.T, directory string) string {
				path := filepath.Join(directory, "unexpected")
				if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "symlinked file",
			mutate: func(t *testing.T, directory string) string {
				path := filepath.Join(directory, "aggregate.json")
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				external := filepath.Join(t.TempDir(), "aggregate.json")
				if err := os.WriteFile(external, contents, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, path); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "nonregular file",
			mutate: func(t *testing.T, directory string) string {
				path := filepath.Join(directory, "aggregate.json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "wrong mode",
			mutate: func(t *testing.T, directory string) string {
				path := filepath.Join(directory, "aggregate.json")
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "multiple hard links",
			mutate: func(t *testing.T, directory string) string {
				path := filepath.Join(directory, "aggregate.json")
				alias := filepath.Join(t.TempDir(), "aggregate.alias")
				if err := os.Link(path, alias); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			seed := newCommitTestGCSeed(t)
			oldGeneration := filepath.Join(
				seed.root, "generations", seed.generations[0],
			)
			probe := testCase.mutate(t, oldGeneration)

			result := seed.commitFourthNormally(t)
			if !result.Committed {
				t.Fatalf("unsafe-orphan commit result = %#v", result)
			}
			if _, err := os.Lstat(oldGeneration); err != nil {
				t.Fatalf("unsafe generation was removed: %v", err)
			}
			if _, err := os.Lstat(probe); err != nil {
				t.Fatalf("unsafe generation contents changed: %v", err)
			}
		})
	}
}

func commitTestRequireAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still exists or has unexpected error: %v", path, err)
	}
}
