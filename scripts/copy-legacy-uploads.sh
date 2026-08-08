#!/usr/bin/env sh
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: copy-legacy-uploads.sh <source-dir> <target-dir>" >&2
  exit 2
fi

source_dir=$1
target_dir=$2
if [ ! -d "$source_dir" ]; then
  echo "legacy uploads source is not a directory: $source_dir" >&2
  exit 2
fi
if [ "$source_dir" = "$target_dir" ]; then
  echo "legacy uploads source and target must differ" >&2
  exit 2
fi

mkdir -p "$target_dir"
# Never overwrite a bind-owned file. A conflicting path is detected by the
# verification pass below and fails closed for operator reconciliation.
# BSD cp reports a non-zero status when -n skips an existing symlink. The
# exhaustive verification below is the owning success signal and still catches
# permission, disk, conflict, and partial-copy failures.
cp -a -n "$source_dir"/. "$target_dir"/ || :

SOURCE_DIR=$source_dir TARGET_DIR=$target_dir
export SOURCE_DIR TARGET_DIR
find "$source_dir" -type f -print0 | xargs -0 sh -c '
  for source_path do
    relative=${source_path#"$SOURCE_DIR"/}
    target_path=$TARGET_DIR/$relative
    if [ ! -f "$target_path" ] || ! cmp -s "$source_path" "$target_path"; then
      echo "legacy upload did not copy exactly: $relative" >&2
      exit 1
    fi
  done
' sh
find "$source_dir" -type l -print0 | xargs -0 sh -c '
  for source_path do
    relative=${source_path#"$SOURCE_DIR"/}
    target_path=$TARGET_DIR/$relative
    if [ ! -L "$target_path" ] || [ "$(readlink "$source_path")" != "$(readlink "$target_path")" ]; then
      echo "legacy upload symlink did not copy exactly: $relative" >&2
      exit 1
    fi
  done
' sh
