#!/bin/sh

write_source_manifest() {
	manifest_root=$1
	manifest_output=$2
	if [ -d "$manifest_output" ]; then
		printf 'source_manifest: output file is a directory: %s\n' "$manifest_output" >&2
		return 1
	fi
	manifest_root_canonical=$(CDPATH= cd "$manifest_root" 2>/dev/null && pwd -P) || return 1
	manifest_output_dir=$(dirname "$manifest_output") || return 1
	manifest_output_base=$(basename "$manifest_output") || return 1
	manifest_output_dir_canonical=$(CDPATH= cd "$manifest_output_dir" 2>/dev/null && pwd -P) || return 1
	manifest_output_canonical=$manifest_output_dir_canonical/$manifest_output_base
	if [ "$manifest_root_canonical" = / ]; then
		printf 'source_manifest: output file must be outside source root: %s\n' "$manifest_output" >&2
		return 1
	fi
	case $manifest_output_canonical in
	"$manifest_root_canonical"|"$manifest_root_canonical"/*)
		printf 'source_manifest: output file must be outside source root: %s\n' "$manifest_output" >&2
		return 1
		;;
	esac
	manifest_unsorted=$(mktemp "$manifest_output_dir/.${manifest_output_base}.unsorted.XXXXXX") || return 1
	manifest_sorted=$(mktemp "$manifest_output_dir/.${manifest_output_base}.sorted.XXXXXX") || {
		rm -f "$manifest_unsorted"
		return 1
	}

	if ! find "$manifest_root" \( -type f -o -type l \) -exec sh -c '
		root=$1
		output=$2
		shift 2
		tab=$(printf "\tX") || exit 1
		tab=${tab%X}
		newline=$(printf "\nX") || exit 1
		newline=${newline%X}
		for file do
			relative_path=${file#"$root"/}
			case $relative_path in
			*"$tab"*|*"$newline"*)
				printf "source_manifest: unsupported tab or newline in path: %s\n" "$relative_path" >&2
				exit 1
				;;
			esac
			if [ -L "$file" ]; then
				git_mode=120000
				hash=$(readlink "$file" | sha256sum) || exit 1
			else
				mode=$(stat -c %a "$file") || exit 1
				case $mode in
					644) git_mode=100644 ;;
					755) git_mode=100755 ;;
					*) git_mode=unsupported-$mode ;;
				esac
				hash=$(sha256sum < "$file") || exit 1
			fi
			hash=${hash%% *}
			printf "%s\t%s\t%s\n" "$relative_path" "$git_mode" "$hash" >> "$output" || exit 1
		done
	' sh "$manifest_root" "$manifest_unsorted" {} +; then
		rm -f "$manifest_unsorted" "$manifest_sorted"
		return 1
	fi

	if ! LC_ALL=C sort "$manifest_unsorted" > "$manifest_sorted"; then
		rm -f "$manifest_unsorted" "$manifest_sorted"
		return 1
	fi
	if ! rm -f "$manifest_unsorted"; then
		rm -f "$manifest_unsorted" "$manifest_sorted"
		return 1
	fi
	if ! mv "$manifest_sorted" "$manifest_output"; then
		rm -f "$manifest_sorted"
		return 1
	fi
}
