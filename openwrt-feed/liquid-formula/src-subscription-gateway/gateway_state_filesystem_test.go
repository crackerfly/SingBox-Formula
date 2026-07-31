package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiskGenerationStoreRequires0700StateDirectories(t *testing.T) {
	for _, target := range []struct {
		name string
		path func(*stateTestFixture) string
	}{
		{name: "root", path: func(f *stateTestFixture) string {
			return f.Root
		}},
		{name: "generations", path: func(f *stateTestFixture) string {
			return f.GenerationsDir
		}},
		{name: "objects", path: func(f *stateTestFixture) string {
			return f.ObjectsDir
		}},
		{name: "generation", path: func(f *stateTestFixture) string {
			return f.GenerationDir
		}},
	} {
		t.Run(target.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			if err := os.Chmod(target.path(fixture), 0755); err != nil {
				t.Fatal(err)
			}
			requireRejectedState(t, fixture.Root)
		})
	}
}

func TestDiskGenerationStoreRequires0600StateFiles(t *testing.T) {
	for _, target := range []struct {
		name string
		path func(*stateTestFixture) string
	}{
		{name: "current", path: func(f *stateTestFixture) string {
			return f.CurrentPath
		}},
		{name: "aggregate", path: func(f *stateTestFixture) string {
			return f.AggregatePath
		}},
		{name: "manifest", path: func(f *stateTestFixture) string {
			return f.ManifestPath
		}},
		{name: "status", path: func(f *stateTestFixture) string {
			return f.StatusPath
		}},
		{name: "object", path: func(f *stateTestFixture) string {
			return f.ObjectPath
		}},
	} {
		t.Run(target.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			if err := os.Chmod(target.path(fixture), 0644); err != nil {
				t.Fatal(err)
			}
			requireRejectedState(t, fixture.Root)
		})
	}
}

func TestDiskGenerationStoreRejectsSymlinkedStateDirectories(t *testing.T) {
	for _, target := range []struct {
		name string
		path func(*stateTestFixture) string
	}{
		{name: "root", path: func(f *stateTestFixture) string {
			return f.Root
		}},
		{name: "generations", path: func(f *stateTestFixture) string {
			return f.GenerationsDir
		}},
		{name: "objects", path: func(f *stateTestFixture) string {
			return f.ObjectsDir
		}},
		{name: "generation", path: func(f *stateTestFixture) string {
			return f.GenerationDir
		}},
	} {
		t.Run(target.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			original := target.path(fixture)
			moved := filepath.Join(
				filepath.Dir(fixture.Root), "moved-"+target.name,
			)
			if err := os.Rename(original, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, original); err != nil {
				t.Fatal(err)
			}
			requireRejectedState(t, fixture.Root)
		})
	}
}

func TestDiskGenerationStoreRejectsSymlinkedStateFiles(t *testing.T) {
	for _, target := range []struct {
		name string
		path func(*stateTestFixture) string
	}{
		{name: "current", path: func(f *stateTestFixture) string {
			return f.CurrentPath
		}},
		{name: "aggregate", path: func(f *stateTestFixture) string {
			return f.AggregatePath
		}},
		{name: "manifest", path: func(f *stateTestFixture) string {
			return f.ManifestPath
		}},
		{name: "status", path: func(f *stateTestFixture) string {
			return f.StatusPath
		}},
		{name: "object", path: func(f *stateTestFixture) string {
			return f.ObjectPath
		}},
	} {
		t.Run(target.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			original := target.path(fixture)
			contents, err := os.ReadFile(original)
			if err != nil {
				t.Fatal(err)
			}
			external := filepath.Join(t.TempDir(), target.name+".real")
			stateTestWriteFile(t, external, contents)
			if err := os.Remove(original); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, original); err != nil {
				t.Fatal(err)
			}
			requireRejectedState(t, fixture.Root)
		})
	}
}

func TestDiskGenerationStoreRejectsNonRegularStateFiles(t *testing.T) {
	for _, target := range []struct {
		name string
		path func(*stateTestFixture) string
	}{
		{name: "current", path: func(f *stateTestFixture) string {
			return f.CurrentPath
		}},
		{name: "aggregate", path: func(f *stateTestFixture) string {
			return f.AggregatePath
		}},
		{name: "manifest", path: func(f *stateTestFixture) string {
			return f.ManifestPath
		}},
		{name: "status", path: func(f *stateTestFixture) string {
			return f.StatusPath
		}},
		{name: "object", path: func(f *stateTestFixture) string {
			return f.ObjectPath
		}},
	} {
		t.Run(target.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			path := target.path(fixture)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0600); err != nil {
				t.Fatal(err)
			}
			requireRejectedState(t, fixture.Root)
		})
	}
}

func TestDiskGenerationStoreRequiresSingleLinkStateFiles(t *testing.T) {
	for _, target := range []struct {
		name string
		path func(*stateTestFixture) string
	}{
		{name: "current", path: func(f *stateTestFixture) string {
			return f.CurrentPath
		}},
		{name: "aggregate", path: func(f *stateTestFixture) string {
			return f.AggregatePath
		}},
		{name: "manifest", path: func(f *stateTestFixture) string {
			return f.ManifestPath
		}},
		{name: "status", path: func(f *stateTestFixture) string {
			return f.StatusPath
		}},
		{name: "object", path: func(f *stateTestFixture) string {
			return f.ObjectPath
		}},
	} {
		t.Run(target.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			alias := filepath.Join(t.TempDir(), target.name+".alias")
			if err := os.Link(target.path(fixture), alias); err != nil {
				t.Fatal(err)
			}
			requireRejectedState(t, fixture.Root)
		})
	}
}

func TestDiskGenerationStoreRequiresExactlyThreeGenerationFiles(t *testing.T) {
	t.Run("extra file", func(t *testing.T) {
		fixture := newStateTestFixture(t, 1)
		stateTestWriteFile(
			t, filepath.Join(fixture.GenerationDir, "unexpected.json"),
			[]byte(`{}`),
		)
		requireRejectedState(t, fixture.Root)
	})

	for _, target := range []struct {
		name string
		path func(*stateTestFixture) string
	}{
		{name: "aggregate missing", path: func(f *stateTestFixture) string {
			return f.AggregatePath
		}},
		{name: "manifest missing", path: func(f *stateTestFixture) string {
			return f.ManifestPath
		}},
		{name: "status missing", path: func(f *stateTestFixture) string {
			return f.StatusPath
		}},
	} {
		t.Run(target.name, func(t *testing.T) {
			fixture := newStateTestFixture(t, 1)
			if err := os.Remove(target.path(fixture)); err != nil {
				t.Fatal(err)
			}
			requireRejectedState(t, fixture.Root)
		})
	}
}
