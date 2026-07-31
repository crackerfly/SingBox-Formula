package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func stateTestReadJSONMap(
	t *testing.T,
	path string,
) map[string]interface{} {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var value map[string]interface{}
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return value
}

func stateTestMarshalJSONMap(
	t *testing.T,
	value map[string]interface{},
) []byte {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON map: %v", err)
	}
	return contents
}

func stateTestObject(
	t *testing.T,
	value interface{},
) map[string]interface{} {
	t.Helper()
	object, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("value is %T, want JSON object", value)
	}
	return object
}

func stateTestArray(t *testing.T, value interface{}) []interface{} {
	t.Helper()
	array, ok := value.([]interface{})
	if !ok {
		t.Fatalf("value is %T, want JSON array", value)
	}
	return array
}

func (fixture *stateTestFixture) writeRawManifest(
	t *testing.T,
	contents []byte,
) {
	t.Helper()
	stateTestWriteFile(t, fixture.ManifestPath, contents)
}

func (fixture *stateTestFixture) writeRawStatus(
	t *testing.T,
	contents []byte,
) {
	t.Helper()
	stateTestWriteFile(t, fixture.StatusPath, contents)
	fixture.Manifest.StatusSHA256 = stateTestSHA256(contents)
	fixture.writeManifest(t)
}

func stateTestAddValidWarning(
	t *testing.T,
	status map[string]interface{},
) map[string]interface{} {
	t.Helper()
	sources := stateTestArray(t, status["sources"])
	source := stateTestObject(t, sources[0])
	source["skipped"] = json.Number("1")
	warning := map[string]interface{}{
		"code":       "invalid_field",
		"node_index": json.Number("1"),
		"type":       "vmess",
		"field":      "port",
	}
	source["warnings"] = []interface{}{warning}
	return warning
}

func TestDiskGenerationStoreRejectsUnknownSchemaFields(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		target string
		mutate func(*testing.T, map[string]interface{})
	}{
		{
			name: "manifest top level", target: "manifest",
			mutate: func(
				t *testing.T, manifest map[string]interface{},
			) {
				manifest["unexpected"] = true
			},
		},
		{
			name: "manifest aggregate", target: "manifest",
			mutate: func(
				t *testing.T, manifest map[string]interface{},
			) {
				aggregate := stateTestObject(t, manifest["aggregate"])
				aggregate["unexpected"] = true
			},
		},
		{
			name: "manifest source", target: "manifest",
			mutate: func(
				t *testing.T, manifest map[string]interface{},
			) {
				sources := stateTestArray(t, manifest["sources"])
				source := stateTestObject(t, sources[0])
				source["unexpected"] = true
			},
		},
		{
			name: "status top level", target: "status",
			mutate: func(t *testing.T, status map[string]interface{}) {
				status["unexpected"] = true
			},
		},
		{
			name: "status source", target: "status",
			mutate: func(t *testing.T, status map[string]interface{}) {
				sources := stateTestArray(t, status["sources"])
				source := stateTestObject(t, sources[0])
				source["unexpected"] = true
			},
		},
		{
			name: "warning", target: "status",
			mutate: func(t *testing.T, status map[string]interface{}) {
				warning := stateTestAddValidWarning(t, status)
				warning["unexpected"] = true
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			path := fixture.ManifestPath
			if testCase.target == "status" {
				path = fixture.StatusPath
			}
			document := stateTestReadJSONMap(t, path)
			testCase.mutate(t, document)
			contents := stateTestMarshalJSONMap(t, document)
			if testCase.target == "status" {
				fixture.writeRawStatus(t, contents)
			} else {
				fixture.writeRawManifest(t, contents)
			}
			requireRejectedState(t, fixture.Root)
		})
	}
}

func TestDiskGenerationStoreRequiresEveryTopLevelSchemaField(t *testing.T) {
	for target, fields := range map[string][]string{
		"manifest": {
			"schema",
			"generation",
			"parent",
			"config_digest",
			"aggregate",
			"status_sha256",
			"sources",
			"legacy_consumed_url_sha256",
		},
		"status": {
			"schema",
			"generation",
			"state",
			"fresh_count",
			"fallback_indices",
			"sources",
		},
	} {
		for _, field := range fields {
			for _, mutation := range []string{"missing", "null"} {
				t.Run(
					target+"/"+field+"/"+mutation,
					func(t *testing.T) {
						fixture := newStateTestFixture(t, 1)
						path := fixture.ManifestPath
						if target == "status" {
							path = fixture.StatusPath
						}
						document := stateTestReadJSONMap(t, path)
						if mutation == "missing" {
							delete(document, field)
						} else {
							document[field] = nil
						}
						contents := stateTestMarshalJSONMap(t, document)
						if target == "status" {
							fixture.writeRawStatus(t, contents)
						} else {
							fixture.writeRawManifest(t, contents)
						}
						requireRejectedState(t, fixture.Root)
					},
				)
			}
		}
	}
}

func TestDiskGenerationStoreRequiresEveryNestedSchemaField(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		target string
		fields []string
		object func(*testing.T, map[string]interface{}) map[string]interface{}
	}{
		{
			name:   "manifest aggregate",
			target: "manifest",
			fields: []string{"sha256", "bytes", "outbounds"},
			object: func(
				t *testing.T, manifest map[string]interface{},
			) map[string]interface{} {
				return stateTestObject(t, manifest["aggregate"])
			},
		},
		{
			name:   "manifest source",
			target: "manifest",
			fields: []string{
				"index", "url_sha256", "object_sha256", "bytes",
				"outbounds",
			},
			object: func(
				t *testing.T, manifest map[string]interface{},
			) map[string]interface{} {
				sources := stateTestArray(t, manifest["sources"])
				return stateTestObject(t, sources[0])
			},
		},
		{
			name:   "status source",
			target: "status",
			fields: []string{
				"index", "result", "fetch_code", "format", "accepted",
				"skipped", "warnings",
			},
			object: func(
				t *testing.T, status map[string]interface{},
			) map[string]interface{} {
				sources := stateTestArray(t, status["sources"])
				return stateTestObject(t, sources[0])
			},
		},
		{
			name:   "warning",
			target: "status",
			fields: []string{"code", "node_index", "type", "field"},
			object: func(
				t *testing.T, status map[string]interface{},
			) map[string]interface{} {
				return stateTestAddValidWarning(t, status)
			},
		},
	} {
		for _, field := range testCase.fields {
			for _, mutation := range []string{"missing", "null"} {
				t.Run(
					testCase.name+"/"+field+"/"+mutation,
					func(t *testing.T) {
						fixture := newStateTestFixture(t, 1)
						path := fixture.ManifestPath
						if testCase.target == "status" {
							path = fixture.StatusPath
						}
						document := stateTestReadJSONMap(t, path)
						object := testCase.object(t, document)
						if mutation == "missing" {
							delete(object, field)
						} else {
							object[field] = nil
						}
						contents := stateTestMarshalJSONMap(t, document)
						if testCase.target == "status" {
							fixture.writeRawStatus(t, contents)
						} else {
							fixture.writeRawManifest(t, contents)
						}
						requireRejectedState(t, fixture.Root)
					},
				)
			}
		}
	}
}

func TestDiskGenerationStoreRejectsTrailingJSONDocuments(t *testing.T) {
	for _, target := range []string{"manifest", "status"} {
		t.Run(target, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			path := fixture.ManifestPath
			if target == "status" {
				path = fixture.StatusPath
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			contents = append(contents, []byte("\n{}")...)
			if target == "status" {
				fixture.writeRawStatus(t, contents)
			} else {
				fixture.writeRawManifest(t, contents)
			}
			requireRejectedState(t, fixture.Root)
		})
	}
}

func TestDiskGenerationStoreEnforces64KiBSchemaBoundary(t *testing.T) {
	for _, target := range []string{"manifest", "status"} {
		for _, testCase := range []struct {
			name  string
			bytes int
			valid bool
		}{
			{name: "exact", bytes: stateSchemaLimit, valid: true},
			{name: "plus one", bytes: stateSchemaLimit + 1, valid: false},
		} {
			t.Run(target+"/"+testCase.name, func(t *testing.T) {
				fixture := newStateTestFixture(t, 1)
				path := fixture.ManifestPath
				if target == "status" {
					path = fixture.StatusPath
				}
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if len(contents) > testCase.bytes {
					t.Fatalf(
						"base %s size = %d, exceeds boundary %d",
						target, len(contents), testCase.bytes,
					)
				}
				contents = append(
					contents,
					bytes.Repeat(
						[]byte(" "), testCase.bytes-len(contents),
					)...,
				)
				if target == "status" {
					fixture.writeRawStatus(t, contents)
				} else {
					fixture.writeRawManifest(t, contents)
				}
				if testCase.valid {
					requireValidState(t, fixture)
				} else {
					requireRejectedState(t, fixture.Root)
				}
			})
		}
	}
}
