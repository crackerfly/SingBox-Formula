#!/bin/sh

set -eu

if [ "$#" -ne 4 ]; then
	printf 'usage: %s TAG EXPECTED_SHA ASSET_DIR NOTES_FILE\n' "$0" >&2
	exit 2
fi

tag=$1
expected_sha=$2
asset_dir=$3
notes_file=$4

: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"

if [ -z "$tag" ] || [ -z "$expected_sha" ]; then
	echo "release tag and expected commit SHA must not be empty" >&2
	exit 2
fi
if [ ! -d "$asset_dir" ]; then
	printf 'release asset directory does not exist: %s\n' "$asset_dir" >&2
	exit 2
fi
if [ ! -f "$notes_file" ]; then
	printf 'release notes file does not exist: %s\n' "$notes_file" >&2
	exit 2
fi

set -- "$asset_dir"/*
if [ ! -e "$1" ]; then
	printf 'release asset directory is empty: %s\n' "$asset_dir" >&2
	exit 2
fi

release_tmp=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/liquid-formula-release.XXXXXX")
tag_error=$release_tmp/tag.error
release_error=$release_tmp/release.error
cleanup()
{
	rm -f -- "$tag_error" "$release_error"
	rmdir "$release_tmp"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

# A 404 means that the object genuinely does not exist. Authentication,
# transport, rate-limit and other API failures must stop publication instead
# of being mistaken for an absent tag/release.
api_object_exists()
{
	endpoint=$1
	error_file=$2

	if gh api "$endpoint" --silent 2>"$error_file"; then
		return 0
	fi
	if grep -Fq 'HTTP 404' "$error_file"; then
		return 1
	fi

	printf 'GitHub API lookup failed for %s:\n' "$endpoint" >&2
	cat "$error_file" >&2
	exit 1
}

tag_exists=false
if api_object_exists \
	"repos/${GITHUB_REPOSITORY}/git/ref/tags/${tag}" "$tag_error"; then
	tag_exists=true
	tag_sha=$(gh api \
		"repos/${GITHUB_REPOSITORY}/commits/tags/${tag}" --jq '.sha')
	if [ "$tag_sha" != "$expected_sha" ]; then
		printf 'refusing to publish %s: tag resolves to %s, build is from %s\n' \
			"$tag" "$tag_sha" "$expected_sha" >&2
		exit 1
	fi
fi

release_exists=false
if api_object_exists \
	"repos/${GITHUB_REPOSITORY}/releases/tags/${tag}" "$release_error"; then
	release_exists=true
fi

if [ "$release_exists" = true ]; then
	if [ "$tag_exists" != true ]; then
		printf 'refusing to update release %s: its tag does not exist\n' "$tag" >&2
		exit 1
	fi
	echo "Release ${tag} already exists at ${expected_sha} — uploading assets."
	gh release upload "$tag" "$asset_dir"/* --clobber
else
	# When no tag exists, --target creates it at the exact commit that produced
	# the matrix. When it already exists, the check above binds it to that same
	# commit before gh attaches a new release to it.
	echo "Creating release ${tag} at ${expected_sha}."
	gh release create "$tag" "$asset_dir"/* \
		--target "$expected_sha" \
		--title "$tag" \
		--notes-file "$notes_file"
fi
