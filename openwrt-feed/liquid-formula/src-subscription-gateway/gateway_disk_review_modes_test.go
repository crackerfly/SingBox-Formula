package main

import (
	"os"
	"testing"
)

var diskReviewSpecialModes = []struct {
	name string
	mode os.FileMode
}{
	{name: "setuid", mode: os.ModeSetuid},
	{name: "setgid", mode: os.ModeSetgid},
	{name: "sticky", mode: os.ModeSticky},
}

func TestDiskGenerationStoreRejectsSpecialDirectoryModeBits(
	t *testing.T,
) {
	targets := []struct {
		name string
		path func(*stateTestFixture) string
	}{
		{
			name: "root",
			path: func(fixture *stateTestFixture) string {
				return fixture.Root
			},
		},
		{
			name: "generations",
			path: func(fixture *stateTestFixture) string {
				return fixture.GenerationsDir
			},
		},
		{
			name: "objects",
			path: func(fixture *stateTestFixture) string {
				return fixture.ObjectsDir
			},
		},
		{
			name: "generation",
			path: func(fixture *stateTestFixture) string {
				return fixture.GenerationDir
			},
		},
	}
	for _, target := range targets {
		for _, special := range diskReviewSpecialModes {
			t.Run(target.name+"/"+special.name, func(t *testing.T) {
				fixture := newStateTestFixture(t, 1)
				path := target.path(fixture)
				diskReviewApplySpecialMode(
					t, path, 0700, special.mode,
				)
				requireRejectedState(t, fixture.Root)
			})
		}
	}
}

func TestDiskGenerationStoreRejectsSpecialFileModeBits(
	t *testing.T,
) {
	targets := []struct {
		name string
		path func(*stateTestFixture) string
	}{
		{
			name: "current",
			path: func(fixture *stateTestFixture) string {
				return fixture.CurrentPath
			},
		},
		{
			name: "aggregate",
			path: func(fixture *stateTestFixture) string {
				return fixture.AggregatePath
			},
		},
		{
			name: "manifest",
			path: func(fixture *stateTestFixture) string {
				return fixture.ManifestPath
			},
		},
		{
			name: "status",
			path: func(fixture *stateTestFixture) string {
				return fixture.StatusPath
			},
		},
		{
			name: "object",
			path: func(fixture *stateTestFixture) string {
				return fixture.ObjectPath
			},
		},
	}
	for _, target := range targets {
		for _, special := range diskReviewSpecialModes {
			t.Run(target.name+"/"+special.name, func(t *testing.T) {
				fixture := newStateTestFixture(t, 1)
				path := target.path(fixture)
				diskReviewApplySpecialMode(
					t, path, 0600, special.mode,
				)
				requireRejectedState(t, fixture.Root)
			})
		}
	}
}

func diskReviewApplySpecialMode(
	t *testing.T,
	path string,
	permissions os.FileMode,
	special os.FileMode,
) {
	t.Helper()
	if err := os.Chmod(path, permissions|special); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if info.Mode().Perm() != permissions ||
		info.Mode()&special == 0 {
		t.Fatalf(
			"%s mode = %s, special bit was not applied",
			path, info.Mode(),
		)
	}
}
