#!/usr/bin/env bash
# Freezes the current tree as a named version:
#
#   bash scripts/snapshot-version.sh v1.1.0 "dark mode contrast, editable names"
#   git push origin main --tags
#
# The copy lands OUTSIDE the repository — ../srvmon-versions/<tag>/ by default,
# or wherever SRVMON_VERSIONS_DIR points — so releases never reach GitHub as
# duplicated source. The git tag is what travels; the folder is a local
# convenience for reading and diffing a release without git.
#
# To go back to one:  bash scripts/snapshot-version.sh --restore v1.1.0
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$PWD"
VERSIONS_DIR="${SRVMON_VERSIONS_DIR:-$(dirname "$REPO_ROOT")/srvmon-versions}"

red=$'\033[0;31m'; green=$'\033[0;32m'; blue=$'\033[0;34m'; plain=$'\033[0m'
info() { echo -e "${green}==>${plain} $*"; }
fail() { echo -e "${red}error:${plain} $*" >&2; exit 1; }

# git decides what belongs in a snapshot: anything it tracks. That keeps the
# copy in step with .gitignore instead of maintaining a second exclude list.
copy_tracked_files() {
  local dest="$1" file
  while IFS= read -r file; do
    [ -n "$file" ] || continue
    mkdir -p "$dest/$(dirname "$file")"
    cp "$file" "$dest/$file"
  done < <(git ls-files)
}

if [ "${1:-}" = "--restore" ]; then
  TAG="${2:-}"
  [ -n "$TAG" ] || fail "usage: $0 --restore <tag>"
  [ -d "$VERSIONS_DIR/$TAG" ] || fail "no snapshot at $VERSIONS_DIR/$TAG"
  [ -z "$(git status --porcelain)" ] || fail "working tree is dirty — commit or stash first"

  info "restoring $TAG over the working tree"
  # Delete tracked files first, so a file the snapshot does not have is removed
  # rather than surviving the copy.
  git ls-files -z | xargs -0 rm -f
  cp -r "$VERSIONS_DIR/$TAG/." .
  rm -f VERSION.md
  git add -A
  info "restored. Review with 'git diff --cached', then commit."
  exit 0
fi

if [ "${1:-}" = "--list" ]; then
  [ -d "$VERSIONS_DIR" ] || { echo "no versions yet at $VERSIONS_DIR"; exit 0; }
  echo "in $VERSIONS_DIR:"
  for dir in "$VERSIONS_DIR"/*/; do
    [ -d "$dir" ] || continue
    tag="$(basename "$dir")"
    note=""
    [ -f "$dir/VERSION.md" ] && note="$(sed -n '3p' "$dir/VERSION.md")"
    printf '  %-10s %s\n' "$tag" "$note"
  done
  exit 0
fi

TAG="${1:-}"
NOTE="${2:-}"
[ -n "$TAG" ] || fail "usage: $0 <tag> [note]   |   $0 --list   |   $0 --restore <tag>"
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "tag must look like v1.2.3"
[ ! -d "$VERSIONS_DIR/$TAG" ] || fail "$VERSIONS_DIR/$TAG already exists"
[ -z "$(git status --porcelain)" ] || fail "working tree is dirty — commit your changes first"
git rev-parse "$TAG" >/dev/null 2>&1 && fail "tag $TAG already exists"

info "copying the tree into $VERSIONS_DIR/$TAG"
mkdir -p "$VERSIONS_DIR/$TAG"
copy_tracked_files "$VERSIONS_DIR/$TAG"

cat > "$VERSIONS_DIR/$TAG/VERSION.md" <<EOF
# $TAG

$NOTE

Frozen $(date -u +%Y-%m-%d) from commit $(git rev-parse --short HEAD).
Restore with: bash scripts/snapshot-version.sh --restore $TAG
EOF

# The tag points at the commit that is already there — nothing is committed for
# a release, so the repository does not grow by one copy of itself per version.
git tag -a "$TAG" -m "${NOTE:-$TAG}"

info "tagged $TAG at $(git rev-parse --short HEAD)"
echo "   local copy: $VERSIONS_DIR/$TAG"
echo "   push it:    git push origin main --tags"
