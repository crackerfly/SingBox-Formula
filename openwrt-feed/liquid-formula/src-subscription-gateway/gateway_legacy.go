package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const legacyMarkerName = "legacy-first-url.sha256"

var errLegacyStateInvalid = errors.New("legacy state invalid")

type legacyReadToken struct {
	markerDevice uint64
	markerInode  uint64
	markerMode   uint32
	markerNlink  uint64
	markerBytes  [diskCurrentBytes]byte
}

type legacyProbeRequest struct {
	Selection      currentSelection
	FirstURLDigest string
}

type legacyProbeResult struct {
	MatchingURLDigest string
	Eligible          bool
	Token             legacyReadToken
}

type legacySourceProvider interface {
	Probe(
		context.Context,
		legacyProbeRequest,
	) (legacyProbeResult, error)
	Load(context.Context, legacyReadToken) ([]byte, error)
	RemoveCommittedMarker(
		context.Context,
		legacyReadToken,
		string,
	) error
}

type fileLegacySourceProvider struct {
	root     string
	nodePath string
}

func newFileLegacySourceProvider(
	root string,
	nodePath string,
) legacySourceProvider {
	return &fileLegacySourceProvider{
		root:     root,
		nodePath: nodePath,
	}
}

func (provider *fileLegacySourceProvider) Probe(
	ctx context.Context,
	request legacyProbeRequest,
) (legacyProbeResult, error) {
	if ctx == nil || ctx.Err() != nil || provider == nil ||
		!isLowerHexDigest(request.FirstURLDigest) ||
		!validCurrentSelection(request.Selection) {
		return legacyProbeResult{}, errLegacyStateInvalid
	}
	root, err := diskOpenDirectoryPath(provider.root)
	if err != nil {
		return legacyProbeResult{}, errLegacyStateInvalid
	}
	defer root.Close()
	token, missing, err := legacyReadMarkerAt(root, legacyMarkerName)
	if err != nil {
		return legacyProbeResult{}, errLegacyStateInvalid
	}
	if missing {
		return legacyProbeResult{}, nil
	}
	markerDigest, valid := token.digest()
	if !valid {
		return legacyProbeResult{}, errLegacyStateInvalid
	}
	if markerDigest != request.FirstURLDigest {
		return legacyProbeResult{}, nil
	}

	eligible := false
	switch request.Selection.Kind {
	case currentAbsent:
		generations, openErr := diskOpenDirectoryAt(
			root, "generations",
		)
		if openErr != nil {
			return legacyProbeResult{}, errLegacyStateInvalid
		}
		names, readErr := generations.Readdirnames(1)
		_ = generations.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return legacyProbeResult{}, errLegacyStateInvalid
		}
		eligible = len(names) == 0
	case currentPresent:
		sources, sourceValid := fallbackSources(
			&request.Selection.Generation,
		)
		if !sourceValid {
			return legacyProbeResult{}, errLegacyStateInvalid
		}
		_, alreadyCached := sources[request.FirstURLDigest]
		alreadyConsumed :=
			request.Selection.Generation.LegacyConsumedURLDigest ==
				request.FirstURLDigest
		eligible = !alreadyCached && !alreadyConsumed
	default:
		return legacyProbeResult{}, errLegacyStateInvalid
	}
	return legacyProbeResult{
		MatchingURLDigest: markerDigest,
		Eligible:          eligible,
		Token:             token,
	}, nil
}

func (provider *fileLegacySourceProvider) Load(
	ctx context.Context,
	token legacyReadToken,
) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || provider == nil ||
		!token.valid() {
		return nil, errLegacyStateInvalid
	}
	root, err := diskOpenDirectoryPath(provider.root)
	if err != nil {
		return nil, errLegacyStateInvalid
	}
	current, missing, err := legacyReadMarkerAt(root, legacyMarkerName)
	_ = root.Close()
	if err != nil || missing || !current.equal(token) {
		return nil, errLegacyStateInvalid
	}
	raw, err := legacyReadNode(provider.nodePath)
	if err != nil {
		return nil, errLegacyStateInvalid
	}
	if ctx.Err() != nil {
		return nil, errLegacyStateInvalid
	}
	return raw, nil
}

func (provider *fileLegacySourceProvider) RemoveCommittedMarker(
	ctx context.Context,
	token legacyReadToken,
	expectedDigest string,
) error {
	if ctx == nil || ctx.Err() != nil || provider == nil ||
		!isLowerHexDigest(expectedDigest) ||
		!token.matchesDigest(expectedDigest) {
		return errLegacyStateInvalid
	}
	root, err := diskOpenDirectoryPath(provider.root)
	if err != nil {
		return errLegacyStateInvalid
	}
	defer root.Close()

	quarantineName := ""
	for attempt := 0; attempt < diskGenerationIDAttempts; attempt++ {
		name := diskPrivateName(".legacy-marker-cleanup-")
		err = unix.Renameat2(
			int(root.Fd()), legacyMarkerName,
			int(root.Fd()), name,
			unix.RENAME_NOREPLACE,
		)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return errLegacyStateInvalid
		}
		quarantineName = name
		break
	}
	if quarantineName == "" {
		return errLegacyStateInvalid
	}

	moved, missing, readErr := legacyReadMarkerAt(
		root, quarantineName,
	)
	if readErr != nil || missing || !moved.equal(token) {
		_ = unix.Renameat2(
			int(root.Fd()), quarantineName,
			int(root.Fd()), legacyMarkerName,
			unix.RENAME_NOREPLACE,
		)
		return errLegacyStateInvalid
	}
	if unix.Unlinkat(int(root.Fd()), quarantineName, 0) != nil ||
		root.Sync() != nil {
		return errLegacyStateInvalid
	}
	return nil
}

func legacyReadMarkerAt(
	root *os.File,
	name string,
) (legacyReadToken, bool, error) {
	if root == nil || name == "" || strings.Contains(name, "/") {
		return legacyReadToken{}, false, errLegacyStateInvalid
	}
	fd, err := unix.Openat(
		int(root.Fd()), name,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if errors.Is(err, unix.ENOENT) {
		return legacyReadToken{}, true, nil
	}
	if err != nil {
		return legacyReadToken{}, false, errLegacyStateInvalid
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil ||
		before.Mode&unix.S_IFMT != unix.S_IFREG ||
		before.Mode&07777 != 0600 ||
		before.Nlink != 1 ||
		before.Size != diskCurrentBytes {
		return legacyReadToken{}, false, errLegacyStateInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, diskCurrentBytes+1))
	if err != nil || len(raw) != diskCurrentBytes {
		return legacyReadToken{}, false, errLegacyStateInvalid
	}
	var after unix.Stat_t
	if unix.Fstat(fd, &after) != nil ||
		!legacySameFileStat(before, after) {
		return legacyReadToken{}, false, errLegacyStateInvalid
	}
	token := legacyReadToken{
		markerDevice: uint64(after.Dev),
		markerInode:  after.Ino,
		markerMode:   after.Mode,
		markerNlink:  uint64(after.Nlink),
	}
	copy(token.markerBytes[:], raw)
	if _, valid := token.digest(); !valid {
		return legacyReadToken{}, false, errLegacyStateInvalid
	}
	return token, false, nil
}

func legacyReadNode(path string) ([]byte, error) {
	cleanPath := filepath.Clean(path)
	name := filepath.Base(cleanPath)
	if !filepath.IsAbs(cleanPath) || name == "." ||
		name == string(os.PathSeparator) ||
		strings.Contains(name, string(os.PathSeparator)) {
		return nil, errLegacyStateInvalid
	}
	parent, err := diskOpenUnrestrictedDirectoryPath(
		filepath.Dir(cleanPath),
	)
	if err != nil {
		return nil, errLegacyStateInvalid
	}
	defer parent.Close()
	fd, err := unix.Openat(
		int(parent.Fd()), name,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, errLegacyStateInvalid
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil ||
		before.Mode&unix.S_IFMT != unix.S_IFREG ||
		before.Mode&07777 != 0644 ||
		before.Nlink != 1 ||
		before.Size < 1 ||
		before.Size > MaxInputBytes {
		return nil, errLegacyStateInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxInputBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > MaxInputBytes ||
		int64(len(raw)) != before.Size {
		return nil, errLegacyStateInvalid
	}
	var after unix.Stat_t
	if unix.Fstat(fd, &after) != nil ||
		!legacySameFileStat(before, after) {
		return nil, errLegacyStateInvalid
	}
	return raw, nil
}

func legacySameFileStat(left unix.Stat_t, right unix.Stat_t) bool {
	return left.Dev == right.Dev &&
		left.Ino == right.Ino &&
		left.Mode == right.Mode &&
		left.Nlink == right.Nlink &&
		left.Size == right.Size
}

func (token legacyReadToken) valid() bool {
	_, valid := token.digest()
	return valid &&
		token.markerDevice != 0 &&
		token.markerInode != 0 &&
		token.markerMode&unix.S_IFMT == unix.S_IFREG &&
		token.markerMode&07777 == 0600 &&
		token.markerNlink == 1
}

func (token legacyReadToken) digest() (string, bool) {
	if token.markerBytes[diskCurrentBytes-1] != '\n' {
		return "", false
	}
	digest := string(token.markerBytes[:diskCurrentBytes-1])
	return digest, isLowerHexDigest(digest)
}

func (token legacyReadToken) matchesDigest(digest string) bool {
	tokenDigest, valid := token.digest()
	return valid && tokenDigest == digest && token.valid()
}

func (token legacyReadToken) equal(other legacyReadToken) bool {
	return token.markerDevice == other.markerDevice &&
		token.markerInode == other.markerInode &&
		token.markerMode == other.markerMode &&
		token.markerNlink == other.markerNlink &&
		bytes.Equal(token.markerBytes[:], other.markerBytes[:])
}
