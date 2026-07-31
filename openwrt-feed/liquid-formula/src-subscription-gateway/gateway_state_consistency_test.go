package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func (fixture *stateTestFixture) rewriteMetadata(t *testing.T) {
	t.Helper()
	fixture.writeStatus(t)
	fixture.writeManifest(t)
}

func (fixture *stateTestFixture) replaceAggregate(
	t *testing.T,
	aggregate []byte,
	outbounds int,
) {
	t.Helper()
	fixture.Aggregate = append([]byte(nil), aggregate...)
	stateTestWriteFile(t, fixture.AggregatePath, fixture.Aggregate)
	fixture.Manifest.Aggregate = stateTestAggregateMetadata{
		SHA256:    stateTestSHA256(fixture.Aggregate),
		Bytes:     len(fixture.Aggregate),
		Outbounds: outbounds,
	}
	fixture.writeManifest(t)
}

func (fixture *stateTestFixture) addObject(
	t *testing.T,
	object []byte,
) (string, string) {
	t.Helper()
	digest := stateTestSHA256(object)
	path := filepath.Join(fixture.ObjectsDir, digest+".json")
	stateTestWriteFile(t, path, object)
	return digest, path
}

func TestDiskGenerationStoreRequiresSchemaOneAndDigestSyntax(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*stateTestFixture)
	}{
		{
			name: "manifest schema",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Schema = 2
			},
		},
		{
			name: "status schema",
			mutate: func(f *stateTestFixture) {
				f.Status.Schema = 2
			},
		},
		{
			name: "generation path agreement",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Generation = strings.Repeat("c", 64)
			},
		},
		{
			name: "status generation agreement",
			mutate: func(f *stateTestFixture) {
				f.Status.Generation = strings.Repeat("c", 64)
			},
		},
		{
			name: "parent digest",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Parent = strings.Repeat("G", 64)
			},
		},
		{
			name: "config digest",
			mutate: func(f *stateTestFixture) {
				f.Manifest.ConfigDigest = strings.Repeat("B", 64)
			},
		},
		{
			name: "url digest",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Sources[0].URLSHA256 = strings.Repeat("C", 64)
			},
		},
		{
			name: "object digest",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Sources[0].ObjectSHA256 =
					strings.Repeat("D", 64)
			},
		},
		{
			name: "status digest",
			mutate: func(f *stateTestFixture) {
				f.Manifest.StatusSHA256 = strings.Repeat("E", 64)
			},
		},
		{
			name: "legacy consumed digest",
			mutate: func(f *stateTestFixture) {
				f.Manifest.LegacyConsumedURLSHA256 =
					strings.Repeat("F", 64)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			testCase.mutate(fixture)
			fixture.rewriteMetadata(t)
			if testCase.name == "status digest" {
				fixture.Manifest.StatusSHA256 = strings.Repeat("e", 64)
				fixture.writeManifest(t)
			}
			requireRejectedState(t, fixture.Root)
		})
	}
}

func TestDiskGenerationStoreAcceptsValidOptionalDigestFields(t *testing.T) {
	fixture := newStateTestFixture(t, 1)
	fixture.Manifest.Parent = strings.Repeat("c", 64)
	fixture.Manifest.LegacyConsumedURLSHA256 = strings.Repeat("d", 64)
	fixture.writeManifest(t)
	requireValidState(t, fixture)
}

func TestDiskGenerationStoreVerifiesEveryHashByteAndOutboundCount(
	t *testing.T,
) {
	for _, testCase := range []struct {
		name   string
		mutate func(*stateTestFixture)
	}{
		{
			name: "aggregate hash",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Aggregate.SHA256 = strings.Repeat("0", 64)
			},
		},
		{
			name: "aggregate bytes",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Aggregate.Bytes++
			},
		},
		{
			name: "aggregate outbounds",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Aggregate.Outbounds++
			},
		},
		{
			name: "status hash",
			mutate: func(f *stateTestFixture) {
				f.Manifest.StatusSHA256 = strings.Repeat("0", 64)
			},
		},
		{
			name: "object content hash",
			mutate: func(f *stateTestFixture) {
				stateTestWriteFile(
					t, f.ObjectPath,
					[]byte(`{"outbounds":[{"tag":"Changed","type":"direct"}]}`),
				)
			},
		},
		{
			name: "object bytes",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Sources[0].Bytes++
			},
		},
		{
			name: "object outbounds",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Sources[0].Outbounds++
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			testCase.mutate(fixture)
			fixture.writeManifest(t)
			requireRejectedState(t, fixture.Root)
		})
	}
}

func TestDiskGenerationStoreValidatesAggregateAndObjectJSONShape(
	t *testing.T,
) {
	t.Run("aggregate invalid JSON", func(t *testing.T) {
		fixture := newStateTestFixture(t, 1)
		fixture.replaceAggregate(t, []byte(`{"outbounds":[`), 1)
		requireRejectedState(t, fixture.Root)
	})

	t.Run("aggregate root shape", func(t *testing.T) {
		fixture := newStateTestFixture(t, 1)
		fixture.replaceAggregate(t, []byte(`[{"type":"direct"}]`), 1)
		requireRejectedState(t, fixture.Root)
	})

	t.Run("aggregate unexpected field", func(t *testing.T) {
		fixture := newStateTestFixture(t, 1)
		fixture.replaceAggregate(
			t,
			[]byte(
				`{"outbounds":[{"tag":"Fixture","type":"direct"}],"extra":true}`,
			),
			1,
		)
		requireRejectedState(t, fixture.Root)
	})

	t.Run("object invalid JSON", func(t *testing.T) {
		fixture := newStateTestFixture(t, 1)
		object := []byte(`{"outbounds":[`)
		digest, _ := fixture.addObject(t, object)
		fixture.Manifest.Sources[0].ObjectSHA256 = digest
		fixture.Manifest.Sources[0].Bytes = len(object)
		fixture.writeManifest(t)
		requireRejectedState(t, fixture.Root)
	})

	t.Run("object root shape", func(t *testing.T) {
		fixture := newStateTestFixture(t, 1)
		object := []byte(`[{"type":"direct"}]`)
		digest, _ := fixture.addObject(t, object)
		fixture.Manifest.Sources[0].ObjectSHA256 = digest
		fixture.Manifest.Sources[0].Bytes = len(object)
		fixture.writeManifest(t)
		requireRejectedState(t, fixture.Root)
	})

	t.Run("object count differs from status", func(t *testing.T) {
		fixture := newStateTestFixture(t, 1)
		object := []byte(
			`{"outbounds":[{"tag":"One","type":"direct"},{"tag":"Two","type":"direct"}]}`,
		)
		digest, _ := fixture.addObject(t, object)
		fixture.Manifest.Sources[0].ObjectSHA256 = digest
		fixture.Manifest.Sources[0].Bytes = len(object)
		fixture.Manifest.Sources[0].Outbounds = 2
		fixture.writeManifest(t)
		requireRejectedState(t, fixture.Root)
	})
}

func TestDiskGenerationStoreRequiresOrderedSourceIndexAgreement(
	t *testing.T,
) {
	for _, testCase := range []struct {
		name   string
		mutate func(*stateTestFixture)
	}{
		{
			name: "manifest index zero",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Sources[0].Index = 0
			},
		},
		{
			name: "manifest index skips",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Sources[1].Index = 3
			},
		},
		{
			name: "status index zero",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[0].Index = 0
			},
		},
		{
			name: "status index skips",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[1].Index = 3
			},
		},
		{
			name: "status source missing",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources = f.Status.Sources[:1]
			},
		},
		{
			name: "manifest source missing",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Sources = f.Manifest.Sources[:1]
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 2)
			testCase.mutate(fixture)
			fixture.rewriteMetadata(t)
			requireRejectedState(t, fixture.Root)
		})
	}
}

func stateTestMakeDistinctFallback(
	t *testing.T,
	fixture *stateTestFixture,
) {
	t.Helper()
	fixture.Manifest.Sources[1].URLSHA256 = stateTestSHA256(
		[]byte("https://fixture.invalid/fallback"),
	)
	fixture.Status.State = generationStateDegraded
	fixture.Status.FreshCount = 1
	fixture.Status.FallbackIndices = []int{2}
	fixture.Status.Sources[1].Result = sourceResultFallback
	fixture.Status.Sources[1].FetchCode = string(fetchCodeTimeout)
	fixture.rewriteMetadata(t)
}

func TestDiskGenerationStoreValidatesFreshAndFallbackStatusAgreement(
	t *testing.T,
) {
	t.Run("valid degraded generation", func(t *testing.T) {
		fixture := newStateTestFixture(t, 2)
		stateTestMakeDistinctFallback(t, fixture)
		requireValidState(t, fixture)
	})

	for _, testCase := range []struct {
		name   string
		mutate func(*stateTestFixture)
	}{
		{
			name: "unknown state",
			mutate: func(f *stateTestFixture) {
				f.Status.State = "partial"
			},
		},
		{
			name: "fresh state has fallback",
			mutate: func(f *stateTestFixture) {
				f.Status.State = generationStateFresh
			},
		},
		{
			name: "fresh count",
			mutate: func(f *stateTestFixture) {
				f.Status.FreshCount = 2
			},
		},
		{
			name: "fallback index mismatch",
			mutate: func(f *stateTestFixture) {
				f.Status.FallbackIndices = []int{1}
			},
		},
		{
			name: "fallback index duplicate",
			mutate: func(f *stateTestFixture) {
				f.Status.FallbackIndices = []int{2, 2}
			},
		},
		{
			name: "fallback index out of range",
			mutate: func(f *stateTestFixture) {
				f.Status.FallbackIndices = []int{3}
			},
		},
		{
			name: "unknown result",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[1].Result = "cached"
			},
		},
		{
			name: "fallback reports ok",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[1].FetchCode = string(fetchCodeOK)
			},
		},
		{
			name: "fresh reports failure",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[0].FetchCode =
					string(fetchCodeHTTPStatus)
			},
		},
		{
			name: "unknown fetch code",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[1].FetchCode = "network_error"
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 2)
			stateTestMakeDistinctFallback(t, fixture)
			testCase.mutate(fixture)
			fixture.rewriteMetadata(t)
			requireRejectedState(t, fixture.Root)
		})
	}

	t.Run("strictly ascending fallback indices", func(t *testing.T) {
		fixture := newStateTestFixture(t, 3)
		for index := 1; index < 3; index++ {
			fixture.Manifest.Sources[index].URLSHA256 = stateTestSHA256(
				[]byte{
					'h', 't', 't', 'p', 's', ':', '/', '/',
					'a' + byte(index), '.', 'i', 'n', 'v', 'a',
					'l', 'i', 'd',
				},
			)
			fixture.Status.Sources[index].Result = sourceResultFallback
			fixture.Status.Sources[index].FetchCode =
				string(fetchCodeTimeout)
		}
		fixture.Status.State = generationStateDegraded
		fixture.Status.FreshCount = 1
		fixture.Status.FallbackIndices = []int{2, 3}
		fixture.rewriteMetadata(t)
		requireValidState(t, fixture)

		fixture.Status.FallbackIndices = []int{3, 2}
		fixture.rewriteMetadata(t)
		requireRejectedState(t, fixture.Root)
	})
}

func TestDiskGenerationStoreValidatesFormatCountsAndWarnings(t *testing.T) {
	for _, format := range []Format{
		FormatSingBoxJSON,
		FormatBase64URI,
		FormatPlainURI,
		FormatClashYAML,
	} {
		t.Run("valid format/"+string(format), func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			fixture.Status.Sources[0].Format = string(format)
			fixture.rewriteMetadata(t)
			requireValidState(t, fixture)
		})
	}

	for _, testCase := range []struct {
		name   string
		mutate func(*stateTestFixture)
	}{
		{
			name: "unknown format",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[0].Format = "yaml"
			},
		},
		{
			name: "accepted zero",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[0].Accepted = 0
			},
		},
		{
			name: "accepted differs from object count",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[0].Accepted = 2
			},
		},
		{
			name: "skipped negative",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[0].Skipped = -1
			},
		},
		{
			name: "too many warnings",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[0].Skipped = 9
				f.Status.Sources[0].Warnings = make([]Warning, 9)
				for index := range f.Status.Sources[0].Warnings {
					f.Status.Sources[0].Warnings[index] = Warning{
						Code:      "invalid_field",
						NodeIndex: index + 1,
						Type:      "vmess",
						Field:     "port",
					}
				}
			},
		},
		{
			name: "unsafe warning code",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[0].Skipped = 1
				f.Status.Sources[0].Warnings = []Warning{{
					Code:      "TOKEN_SECRET_CANARY",
					NodeIndex: 1,
					Type:      "vmess",
					Field:     "port",
				}}
			},
		},
		{
			name: "unsafe warning type",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[0].Skipped = 1
				f.Status.Sources[0].Warnings = []Warning{{
					Code:      "invalid_field",
					NodeIndex: 1,
					Type:      "UUID_SECRET_CANARY",
					Field:     "port",
				}}
			},
		},
		{
			name: "unsafe warning field",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[0].Skipped = 1
				f.Status.Sources[0].Warnings = []Warning{{
					Code:      "invalid_field",
					NodeIndex: 1,
					Type:      "vmess",
					Field:     "PASSWORD_SECRET_CANARY",
				}}
			},
		},
		{
			name: "warning index zero",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[0].Skipped = 1
				f.Status.Sources[0].Warnings = []Warning{{
					Code:      "invalid_field",
					NodeIndex: 0,
					Type:      "vmess",
					Field:     "port",
				}}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			testCase.mutate(fixture)
			fixture.rewriteMetadata(t)
			requireRejectedState(t, fixture.Root)
		})
	}

	t.Run("eight warnings allowed", func(t *testing.T) {
		fixture := newStateTestFixture(t, 1)
		fixture.Status.Sources[0].Skipped = 8
		fixture.Status.Sources[0].Warnings = make([]Warning, 8)
		for index := range fixture.Status.Sources[0].Warnings {
			fixture.Status.Sources[0].Warnings[index] = Warning{
				Code:      "invalid_field",
				NodeIndex: index + 1,
				Type:      "vmess",
				Field:     "port",
			}
		}
		fixture.rewriteMetadata(t)
		requireValidState(t, fixture)
	})

	t.Run("empty safe warning strings remain explicit", func(t *testing.T) {
		fixture := newStateTestFixture(t, 1)
		status := stateTestReadJSONMap(t, fixture.StatusPath)
		sources := stateTestArray(t, status["sources"])
		source := stateTestObject(t, sources[0])
		source["skipped"] = json.Number("1")
		source["warnings"] = []interface{}{map[string]interface{}{
			"code":       "invalid_field",
			"node_index": json.Number("1"),
			"type":       "",
			"field":      "",
		}}
		fixture.writeRawStatus(t, stateTestMarshalJSONMap(t, status))
		requireValidState(t, fixture)
	})
}

func TestDiskGenerationStoreBoundsOrderedSourceOccurrences(t *testing.T) {
	t.Run("eight", func(t *testing.T) {
		fixture := newStateTestFixture(t, 8)
		requireValidState(t, fixture)
	})
	t.Run("nine", func(t *testing.T) {
		fixture := newStateTestFixture(t, 9)
		requireRejectedState(t, fixture.Root)
	})
}

func TestDiskGenerationStoreRequiresDuplicateURLMetadataToMatch(
	t *testing.T,
) {
	t.Run("identical duplicate is valid", func(t *testing.T) {
		fixture := newStateTestFixture(t, 2)
		requireValidState(t, fixture)
	})

	for _, testCase := range []struct {
		name   string
		mutate func(*stateTestFixture)
	}{
		{
			name: "object",
			mutate: func(f *stateTestFixture) {
				object := []byte(
					`{"outbounds":[{"tag":"Other","type":"direct"}]}`,
				)
				digest, _ := f.addObject(t, object)
				f.Manifest.Sources[1].ObjectSHA256 = digest
				f.Manifest.Sources[1].Bytes = len(object)
				f.replaceAggregate(
					t,
					[]byte(
						`{"outbounds":[{"tag":"Fixture","type":"direct"},{"tag":"Other","type":"direct"}]}`,
					),
					2,
				)
			},
		},
		{
			name: "format",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[1].Format = string(FormatPlainURI)
			},
		},
		{
			name: "bytes",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Sources[1].Bytes++
			},
		},
		{
			name: "outbounds",
			mutate: func(f *stateTestFixture) {
				f.Manifest.Sources[1].Outbounds++
			},
		},
		{
			name: "accepted",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[1].Accepted++
			},
		},
		{
			name: "result and fetch code",
			mutate: func(f *stateTestFixture) {
				f.Status.State = generationStateDegraded
				f.Status.FreshCount = 1
				f.Status.FallbackIndices = []int{2}
				f.Status.Sources[1].Result = sourceResultFallback
				f.Status.Sources[1].FetchCode =
					string(fetchCodeTimeout)
			},
		},
		{
			name: "normalize counts and warnings",
			mutate: func(f *stateTestFixture) {
				f.Status.Sources[1].Skipped = 1
				f.Status.Sources[1].Warnings = []Warning{{
					Code:      "invalid_field",
					NodeIndex: 1,
					Type:      "vmess",
					Field:     "port",
				}}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 2)
			testCase.mutate(fixture)
			fixture.rewriteMetadata(t)
			requireRejectedState(t, fixture.Root)
		})
	}
}

func TestDiskGenerationStoreLoadsOneObjectBackingPerDigest(t *testing.T) {
	fixture := newStateTestFixture(t, 2)
	fixture.Manifest.Sources[1].URLSHA256 = stateTestSHA256(
		[]byte("https://fixture.invalid/second"),
	)
	fixture.rewriteMetadata(t)

	selection := requireValidState(t, fixture)
	sources := selection.Generation.Sources
	if len(sources) != 2 {
		t.Fatalf("source count = %d, want 2", len(sources))
	}
	if !bytes.Equal(sources[0].Normalized, sources[1].Normalized) {
		t.Fatal("same object digest loaded different bytes")
	}
	if len(sources[0].Normalized) == 0 ||
		&sources[0].Normalized[0] != &sources[1].Normalized[0] {
		t.Fatal("same object digest did not share one loaded backing")
	}
}

func TestObserveCurrentValidatesTheCompleteGenerationBeforePreserved(
	t *testing.T,
) {
	for _, testCase := range []struct {
		name    string
		corrupt func(*testing.T, *stateTestFixture)
	}{
		{
			name: "manifest",
			corrupt: func(t *testing.T, f *stateTestFixture) {
				stateTestWriteFile(t, f.ManifestPath, []byte(`{}`))
			},
		},
		{
			name: "status",
			corrupt: func(t *testing.T, f *stateTestFixture) {
				stateTestWriteFile(t, f.StatusPath, []byte(`{}`))
			},
		},
		{
			name: "aggregate",
			corrupt: func(t *testing.T, f *stateTestFixture) {
				stateTestWriteFile(t, f.AggregatePath, []byte(`{}`))
			},
		},
		{
			name: "object",
			corrupt: func(t *testing.T, f *stateTestFixture) {
				stateTestWriteFile(t, f.ObjectPath, []byte(`{}`))
			},
		},
		{
			name: "generation layout",
			corrupt: func(t *testing.T, f *stateTestFixture) {
				stateTestWriteFile(
					t, filepath.Join(f.GenerationDir, "extra"), []byte(`x`),
				)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			testCase.corrupt(t, fixture)
			store := stateTestStore(fixture.Root)
			observation, _ := store.ObserveCurrent(context.Background())
			if observation.Validated {
				t.Fatalf(
					"corrupt %s was marked preserved: %#v",
					testCase.name, observation,
				)
			}
		})
	}
}
