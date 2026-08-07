#!/usr/bin/env bash
# Freezes the current tree as a named version:
#
#   bash scripts/snapshot-version.sh v1.1.0 "dark mode contrast, editable names"
#
# It writes versions/<tag>/ as a plain copy you can read or diff without git,
# commits it, and tags the commit so GitHub carries the same version.
#
# To go back to one:  bash scripts/snapshot-version.sh --restore v1.1.0
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

VERSIONS_DIR="versions"
# Everything that is generated, local, or would make the snapshot recursive.
EXCLUDES=(".git" "$VERSIONS_DIR" "bin" "node_modules" "*.db" "*.db-wal" "*.db-shm")

red=$'\033[0;31m'; green=$'\033[0;32m'; yellow=$'\033[0;33m'; plain=$'\033[0m'
info() { echo -e "${green}==>${plain} $*"; }
fail() { echo -e "${red}error:${plain} $*" >&2; exit 1; }

copy_tree() {
  local dest="$1" args=()
  local pattern
  for pattern in "${EXCLUDES[@]}"; do args+=(--exclude="$pattern"); done
  if command -v rsync >/dev/null 2>&1; then
    rsync -a "${args[@]}" ./ "$dest/"
  else
    # tar is everywhere rsync is not, and honours the same exclude patterns.
    local tar_args=()
    for pattern in "${EXCLUDES[@]}"; do tar_args+=(--exclude="$pattern"); done
    mkdir -p "$dest"
    tar -cf - "${tar_args[@]}" . | tar -xf - -C "$dest"
  fi
}

if [ "${1:-}" = "--restore" ]; then
  TAG="${2:-}"
  [ -n "$TAG" ] || fail "usage: $0 --restore <tag>"
  [ -d "$VERSIONS_DIR/$TAG" ] || fail "no snapshot at $VERSIONS_DIR/$TAG"
  [ -z "$(git status --porcelain)" ] || fail "working tree is dirty — commit or stash first"

  info "restoring $TAG over the working tree"
  local_files=$(git ls-files | grep -v "^$VERSIONS_DIR/" || true)
  # Delete tracked files first so a file that the snapshot does not have is
  # actually removed, rather than surviving the copy.
  echo "$local_files" | while read -r file; do [ -n "$file" ] && rm -f "$file"; done
  cp -r "$VERSIONS_DIR/$TAG/." .
  git add -A
  info "restored. Review with 'git diff --cached', then commit."
  exit 0
fi

if [ "${1:-}" = "--list" ]; then
  [ -d "$VERSIONS_DIR" ] || { echo "no versions yet"; exit 0; }
  for dir in "$VERSIONS_DIR"/*/; do
    [ -d "$dir" ] || continue
    tag="$(basename "$dir")"
    note=""
    [ -f "$dir/VERSION.md" ] && note="$(sed -n '3p' "$dir/VERSION.md")"
    printf '%-12s %s\n' "$tag" "$note"
  done
  exit 0
fi

TAG="${1:-}"
NOTE="${2:-}"
[ -n "$TAG" ] || fail "usage: $0 <tag> [note]   |   $0 --list   |   $0 --restore <tag>"
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "tag must look like v1.2.3"
[ ! -d "$VERSIONS_DIR/$TAG" ] || fail "$VERSIONS_DIR/$TAG already exists"
[ -z "$(git status --porcelain)" ] || fail "working tree is dirty — commit your changes first"

info "snapshotting the tree into $VERSIONS_DIR/$TAG"
mkdir -p "$VERSIONS_DIR/$TAG"
copy_tree "$VERSIONS_DIR/$TAG"

cat > "$VERSIONS_DIR/$TAG/VERSION.md" <<EOF
# $TAG

$NOTE

Frozen $(date -u +%Y-%m-%d) from commit $(git rev-parse --short HEAD).
Restore with: bash scripts/snapshot-version.sh --restore $TAG
EOF

git add "$VERSIONS_DIR/$TAG"
git commit -q -m "chore(release): snapshot $TAG

${NOTE:-Frozen copy of the tree at this version.}"
git tag -a "$TAG" -m "${NOTE:-$TAG}"

info "committed and tagged $TAG"
echo "   push it with:  git push origin main --tags"
