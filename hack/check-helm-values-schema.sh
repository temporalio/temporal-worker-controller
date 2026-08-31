#!/usr/bin/env bash
# Fail when helm/temporal-worker-controller/values.yaml has non-comment changes
# but values.schema.json was not updated in the same range.
#
# CI (pull_request): compare against the PR base SHA.
# CI (push): compare against the previous commit on the branch.
# Local: compare against the merge-base with origin/main (or main).
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
VALUES="helm/temporal-worker-controller/values.yaml"
SCHEMA="helm/temporal-worker-controller/values.schema.json"

resolve_base() {
  if [[ -n "${PR_BASE_SHA:-}" ]]; then
    echo "${PR_BASE_SHA}"
    return
  fi

  if [[ -n "${PUSH_BEFORE_SHA:-}" && "${PUSH_BEFORE_SHA}" != "0000000000000000000000000000000000000000" ]]; then
    echo "${PUSH_BEFORE_SHA}"
    return
  fi

  local candidate
  for candidate in "${BASE_REF:-}" origin/main main; do
    if [[ -n "${candidate}" ]] && git rev-parse --verify --quiet "${candidate}^{commit}" >/dev/null; then
      git merge-base HEAD "${candidate}"
      return
    fi
  done

  echo "unable to determine a base commit; set PR_BASE_SHA, PUSH_BEFORE_SHA, or BASE_REF" >&2
  exit 2
}

# Print non-comment, non-blank added/removed lines from a unified diff.
non_comment_diff_lines() {
  grep -E '^[+-]' \
    | grep -vE '^[+-]{3} ' \
    | sed 's/^[+-]//' \
    | grep -vE '^[[:space:]]*$' \
    | grep -vE '^[[:space:]]*#' || true
}

# Compare against the working tree so the check works locally and in CI
# (CI checkouts are clean, so this matches HEAD).
file_changed() {
  local base="$1"
  local path="$2"
  ! git diff --quiet "${base}" -- "${path}"
}

cd "${ROOT}"

base="$(resolve_base)"
if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
  echo "base commit ${base} is not available in this clone; fetch the PR base or main history" >&2
  exit 2
fi

if ! file_changed "${base}" "${VALUES}"; then
  echo "No changes to ${VALUES} since ${base}; schema check skipped."
  exit 0
fi

values_diff="$(git diff -U0 "${base}" -- "${VALUES}")"
if [[ -z "$(printf '%s\n' "${values_diff}" | non_comment_diff_lines)" ]]; then
  echo "Only comment/whitespace changes in ${VALUES} since ${base}; schema check skipped."
  exit 0
fi

if file_changed "${base}" "${SCHEMA}"; then
  echo "${VALUES} and ${SCHEMA} both changed since ${base}."
  exit 0
fi

echo "error: ${VALUES} has non-comment changes since ${base}, but ${SCHEMA} was not updated." >&2
echo "Update ${SCHEMA} to match the new values (or include it in this PR)." >&2
echo >&2
echo "Non-comment values.yaml diff:" >&2
printf '%s\n' "${values_diff}" >&2
exit 1
