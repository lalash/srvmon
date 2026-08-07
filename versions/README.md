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

## A note on the duplication

These folders repeat the whole tree, so the repository grows by roughly its own
size per version. The git tags alone already allow rollback, and
`git checkout <tag>` costs nothing. The folders exist because a plain copy is
easier to browse and compare than a tag, which is a fair trade at this size —
but if the repository ever feels heavy, the folders can be deleted and the tags
kept without losing any history.
