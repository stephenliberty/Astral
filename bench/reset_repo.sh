#!/usr/bin/env bash
# Reset a target repo to a pristine checkout and re-index with astral.
# Used between benchmark iterations so no prior run leaks state.
#
# Usage: reset_repo.sh <repo> [astral_bin]
set -euo pipefail

REPO="${1:?repo path required}"
ASTRAL="${2:-astral}"

# Preserve human-authored notes across the reset (they are committable state).
NOTES_BACKUP="/tmp/astral-notes-backup"
rm -rf "$NOTES_BACKUP"
if [ -d "$REPO/.astral/notes" ]; then
  cp -r "$REPO/.astral/notes" "$NOTES_BACKUP"
fi

# Reset tracked files and drop untracked artifacts the model may have created,
# but preserve dependency installs (node_modules, .yarn, .pnpm-store) so TS/JS
# repos don't need a reinstall on every iteration.
git -C "$REPO" checkout -- . 2>/dev/null || true
git -C "$REPO" clean -fd \
  -e "node_modules/" -e "**/node_modules/" \
  -e ".yarn/" -e ".pnpm/" -e ".pnpm-store/" \
  -- . >/dev/null 2>&1 || true

# Re-index fresh, then restore notes so advisor runs keep their invariants.
"$ASTRAL" init "$REPO" >/dev/null 2>&1 || true
if [ -d "$NOTES_BACKUP" ]; then
  mkdir -p "$REPO/.astral/notes"
  cp -r "$NOTES_BACKUP/." "$REPO/.astral/notes/"
fi
