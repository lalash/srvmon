# versions/

Each subdirectory is a frozen copy of the whole tree at the moment a version was
declared good, so a version can be read, diffed or restored without knowing git.
Every snapshot is also a git tag of the same name, which is what GitHub shows
under Releases.

## Freezing the current tree

```bash
bash scripts/snapshot-version.sh v1.1.0 "what changed in this version"
git push origin main --tags
```

The working tree has to be committed first — the snapshot is of what is
committed, not of half-finished edits.

## Going back to one

```bash
bash scripts/snapshot-version.sh --restore v1.0.0
git diff --cached      # review what going back changes
git commit -m "revert to v1.0.0"
```

Restoring stages the older files over the working tree without touching history,
so nothing is lost if you change your mind before committing. `git checkout
v1.0.0` reaches the same code through the tag if you would rather use git
directly.

```bash
bash scripts/snapshot-version.sh --list
```

## Numbering

`vMAJOR.MINOR.PATCH`. Patch for fixes, minor for features that do not break an
existing install, major for anything that needs manual steps on a running hub.

## What a version actually costs

A snapshot holds only the source: `versions/` is excluded from the copy, so
`versions/v1.1.0/` never contains `versions/v1.0.0/`. Nothing nests, and nothing
grows exponentially.

Measured on this repository, three versions in:

| | |
| --- | --- |
| git object store, snapshot with no source change | +5 KB |
| git object store, snapshot with one file changed | +4 KB |
| checkout on disk, per version | +330 KB |

The repository barely grows because git stores a blob once per distinct content,
however many paths point at it — a new version only adds the directory listings.
The working copy is what grows linearly, and at this size that is a rounding
error. Should it ever matter, the folders can be deleted and the tags kept
without losing any history.
