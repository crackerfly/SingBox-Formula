package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyMarkerProbeRequiresExactSafeMarker(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	t.Run("exact marker", func(t *testing.T) {
		root, nodePath := legacyTestPrepareProviderRoot(t)
		legacyTestWriteMarker(t, root, digest+"\n")
		provider := newFileLegacySourceProvider(root, nodePath)

		probe, err := provider.Probe(
			context.Background(),
			legacyProbeRequest{
				Selection:      currentSelection{Kind: currentAbsent},
				FirstURLDigest: digest,
			},
		)
		if err != nil {
			t.Fatalf("exact marker probe failed: %v", err)
		}
		if probe.MatchingURLDigest != digest || !probe.Eligible {
			t.Fatalf("exact marker probe = %#v", probe)
		}
	})

	t.Run("missing marker is simply unavailable", func(t *testing.T) {
		root, nodePath := legacyTestPrepareProviderRoot(t)
		provider := newFileLegacySourceProvider(root, nodePath)
		probe, err := provider.Probe(
			context.Background(),
			legacyProbeRequest{
				Selection:      currentSelection{Kind: currentAbsent},
				FirstURLDigest: digest,
			},
		)
		if err != nil || probe.MatchingURLDigest != "" ||
			probe.Eligible {
			t.Fatalf("missing marker probe = %#v err=%v", probe, err)
		}
	})

	t.Run("different valid digest does not match", func(t *testing.T) {
		root, nodePath := legacyTestPrepareProviderRoot(t)
		legacyTestWriteMarker(
			t, root,
			"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n",
		)
		provider := newFileLegacySourceProvider(root, nodePath)
		probe, err := provider.Probe(
			context.Background(),
			legacyProbeRequest{
				Selection:      currentSelection{Kind: currentAbsent},
				FirstURLDigest: digest,
			},
		)
		if err != nil || probe.MatchingURLDigest != "" ||
			probe.Eligible {
			t.Fatalf("different marker probe = %#v err=%v", probe, err)
		}
	})

	for _, testCase := range []struct {
		name     string
		contents string
	}{
		{name: "uppercase", contents: strings.Repeat("A", 64) + "\n"},
		{name: "missing newline", contents: digest},
		{name: "extra newline", contents: digest + "\n\n"},
		{name: "short digest", contents: strings.Repeat("a", 63) + "\n"},
		{name: "non hex", contents: strings.Repeat("g", 64) + "\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root, nodePath := legacyTestPrepareProviderRoot(t)
			legacyTestWriteMarker(t, root, testCase.contents)
			provider := newFileLegacySourceProvider(root, nodePath)
			if _, err := provider.Probe(
				context.Background(),
				legacyProbeRequest{
					Selection:      currentSelection{Kind: currentAbsent},
					FirstURLDigest: digest,
				},
			); err == nil {
				t.Fatalf("unsafe marker %q was accepted", testCase.name)
			}
		})
	}

	t.Run("mode 0644", func(t *testing.T) {
		root, nodePath := legacyTestPrepareProviderRoot(t)
		marker := legacyTestWriteMarker(t, root, digest+"\n")
		if err := os.Chmod(marker, 0644); err != nil {
			t.Fatal(err)
		}
		provider := newFileLegacySourceProvider(root, nodePath)
		if _, err := provider.Probe(
			context.Background(),
			legacyProbeRequest{
				Selection:      currentSelection{Kind: currentAbsent},
				FirstURLDigest: digest,
			},
		); err == nil {
			t.Fatal("world-readable marker was accepted")
		}
	})

	t.Run("symlink is not followed", func(t *testing.T) {
		root, nodePath := legacyTestPrepareProviderRoot(t)
		victim := filepath.Join(t.TempDir(), "MARKER_SECRET_PATH_CANARY")
		if err := os.WriteFile(victim, []byte(digest+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		marker := legacyTestMarkerPath(root)
		if err := os.Symlink(victim, marker); err != nil {
			t.Fatal(err)
		}
		provider := newFileLegacySourceProvider(root, nodePath)
		_, err := provider.Probe(
			context.Background(),
			legacyProbeRequest{
				Selection:      currentSelection{Kind: currentAbsent},
				FirstURLDigest: digest,
			},
		)
		if err == nil {
			t.Fatal("marker symlink was followed")
		}
		if strings.Contains(err.Error(), victim) ||
			strings.Contains(err.Error(), "MARKER_SECRET_PATH_CANARY") {
			t.Fatalf("marker error leaked path: %v", err)
		}
	})

	t.Run("hard link is rejected", func(t *testing.T) {
		root, nodePath := legacyTestPrepareProviderRoot(t)
		marker := legacyTestWriteMarker(t, root, digest+"\n")
		if err := os.Link(marker, filepath.Join(root, "marker-hardlink")); err != nil {
			t.Fatal(err)
		}
		provider := newFileLegacySourceProvider(root, nodePath)
		if _, err := provider.Probe(
			context.Background(),
			legacyProbeRequest{
				Selection:      currentSelection{Kind: currentAbsent},
				FirstURLDigest: digest,
			},
		); err == nil {
			t.Fatal("multiply linked marker was accepted")
		}
	})

	t.Run("non regular marker is rejected", func(t *testing.T) {
		root, nodePath := legacyTestPrepareProviderRoot(t)
		if err := os.Mkdir(legacyTestMarkerPath(root), 0600); err != nil {
			t.Fatal(err)
		}
		provider := newFileLegacySourceProvider(root, nodePath)
		if _, err := provider.Probe(
			context.Background(),
			legacyProbeRequest{
				Selection:      currentSelection{Kind: currentAbsent},
				FirstURLDigest: digest,
			},
		); err == nil {
			t.Fatal("marker directory was accepted")
		}
	})
}

func TestLegacyProbeAllowsAbsentAdoptionOnlyBeforeAnyGeneration(
	t *testing.T,
) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	root, nodePath := legacyTestPrepareProviderRoot(t)
	legacyTestWriteMarker(t, root, digest+"\n")
	provider := newFileLegacySourceProvider(root, nodePath)

	probe, err := provider.Probe(
		context.Background(),
		legacyProbeRequest{
			Selection:      currentSelection{Kind: currentAbsent},
			FirstURLDigest: digest,
		},
	)
	if err != nil || !probe.Eligible ||
		probe.MatchingURLDigest != digest {
		t.Fatalf("empty-state probe = %#v err=%v", probe, err)
	}

	generationPath := filepath.Join(
		root, "generations", strings.Repeat("1", 64),
	)
	if err := os.Mkdir(generationPath, 0700); err != nil {
		t.Fatal(err)
	}
	probe, err = provider.Probe(
		context.Background(),
		legacyProbeRequest{
			Selection:      currentSelection{Kind: currentAbsent},
			FirstURLDigest: digest,
		},
	)
	if err != nil {
		t.Fatalf("retained-generation probe failed: %v", err)
	}
	if probe.MatchingURLDigest != digest || probe.Eligible {
		t.Fatalf("retained generation did not disable adoption: %#v", probe)
	}

	probe, err = provider.Probe(
		context.Background(),
		legacyProbeRequest{
			Selection: currentSelection{
				Kind:       currentPresent,
				Generation: legacyTestGeneration("", nil),
			},
			FirstURLDigest: digest,
		},
	)
	if err != nil || !probe.Eligible {
		t.Fatalf("valid-present probe = %#v err=%v", probe, err)
	}

	if _, err := provider.Probe(
		context.Background(),
		legacyProbeRequest{
			Selection:      currentSelection{Kind: currentInvalid},
			FirstURLDigest: digest,
		},
	); err == nil {
		t.Fatal("invalid current permitted legacy adoption")
	}
}

func TestLegacyMarkerReceiptRejectsProbeLoadReplacement(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	root, nodePath := legacyTestPrepareProviderRoot(t)
	legacyTestWriteMarker(t, root, digest+"\n")
	legacyNode := []byte(
		`{"outbounds":[{"type":"direct","tag":"Legacy"}]}`,
	)
	if err := os.WriteFile(nodePath, legacyNode, 0600); err != nil {
		t.Fatal(err)
	}
	provider := newFileLegacySourceProvider(root, nodePath)
	probe, err := provider.Probe(
		context.Background(),
		legacyProbeRequest{
			Selection:      currentSelection{Kind: currentAbsent},
			FirstURLDigest: digest,
		},
	)
	if err != nil || !probe.Eligible {
		t.Fatalf("initial probe = %#v err=%v", probe, err)
	}

	replacement := filepath.Join(root, "replacement-marker")
	if err := os.WriteFile(replacement, []byte(digest+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, legacyTestMarkerPath(root)); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Load(context.Background(), probe.Token); err == nil {
		t.Fatal("Load accepted a marker replaced after Probe")
	}
	if err := provider.RemoveCommittedMarker(
		context.Background(), probe.Token, digest,
	); err == nil {
		t.Fatal("cleanup removed a marker replaced after Probe")
	}
	got, err := os.ReadFile(legacyTestMarkerPath(root))
	if err != nil || !bytes.Equal(got, []byte(digest+"\n")) {
		t.Fatalf("replacement marker changed: %q err=%v", got, err)
	}
}

func TestLegacyProviderLoadsExactRegularConverterCache(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	raw := []byte(
		`{"outbounds":[{"type":"direct","tag":"Existing Cache"}]}`,
	)
	root, nodePath := legacyTestPrepareProviderRoot(t)
	legacyTestWriteMarker(t, root, digest+"\n")
	// The frozen converter publishes node.json with mode 0644.
	if err := os.WriteFile(nodePath, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(nodePath, 0644); err != nil {
		t.Fatal(err)
	}
	provider := newFileLegacySourceProvider(root, nodePath)
	probe, err := provider.Probe(
		context.Background(),
		legacyProbeRequest{
			Selection:      currentSelection{Kind: currentAbsent},
			FirstURLDigest: digest,
		},
	)
	if err != nil || !probe.Eligible {
		t.Fatalf("probe = %#v err=%v", probe, err)
	}
	got, err := provider.Load(context.Background(), probe.Token)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("legacy node=%q err=%v", got, err)
	}

	victim := filepath.Join(t.TempDir(), "LEGACY_NODE_PATH_SECRET_CANARY")
	if err := os.WriteFile(victim, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(nodePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, nodePath); err != nil {
		t.Fatal(err)
	}
	_, err = provider.Load(context.Background(), probe.Token)
	if err == nil {
		t.Fatal("legacy node symlink was followed")
	}
	if strings.Contains(err.Error(), victim) ||
		strings.Contains(err.Error(), "LEGACY_NODE_PATH_SECRET_CANARY") {
		t.Fatalf("legacy node error leaked path: %v", err)
	}
}

func TestLegacyMarkerReceiptBindsExactBytesAndMode(t *testing.T) {
	const (
		digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		digestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	for _, testCase := range []struct {
		name   string
		mutate func(string) error
	}{
		{
			name: "same inode different exact bytes",
			mutate: func(path string) error {
				return os.WriteFile(path, []byte(digestB+"\n"), 0600)
			},
		},
		{
			name: "same inode unsafe mode",
			mutate: func(path string) error {
				return os.Chmod(path, 0644)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root, nodePath := legacyTestPrepareProviderRoot(t)
			marker := legacyTestWriteMarker(t, root, digestA+"\n")
			if err := os.WriteFile(
				nodePath,
				[]byte(`{"outbounds":[{"type":"direct"}]}`),
				0600,
			); err != nil {
				t.Fatal(err)
			}
			provider := newFileLegacySourceProvider(root, nodePath)
			probe, err := provider.Probe(
				context.Background(),
				legacyProbeRequest{
					Selection:      currentSelection{Kind: currentAbsent},
					FirstURLDigest: digestA,
				},
			)
			if err != nil || !probe.Eligible {
				t.Fatalf("initial probe = %#v err=%v", probe, err)
			}
			if err := testCase.mutate(marker); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.Load(
				context.Background(), probe.Token,
			); err == nil {
				t.Fatal("Load accepted a marker changed after Probe")
			}
			if err := provider.RemoveCommittedMarker(
				context.Background(), probe.Token, digestA,
			); err == nil {
				t.Fatal("cleanup accepted a marker changed after Probe")
			}
			if _, err := os.Lstat(marker); err != nil {
				t.Fatalf("changed marker was removed: %v", err)
			}
		})
	}
}

func legacyTestPrepareProviderRoot(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "subscriptions")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "generations"), 0700); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(base, "legacy-cache")
	if err := os.Mkdir(cache, 0700); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(cache, "node.json")
}

func legacyTestMarkerPath(root string) string {
	return filepath.Join(root, "legacy-first-url.sha256")
}

func legacyTestWriteMarker(
	t *testing.T,
	root string,
	contents string,
) string {
	t.Helper()
	path := legacyTestMarkerPath(root)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
