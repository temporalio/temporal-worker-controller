#!/usr/bin/env bash
# check-helm-values-schema.sh compares helm/temporal-worker-controller/values.yaml
# between the current working tree and the Git ref in GIT_REF.
# If any non-comment changes have been made to values.yaml, values.schema.json
# is checked for a non-empty diff. If that diff is NOT empty, the script
# returns 0 (success); otherwise it returns a non-zero exit code.
#
# GIT_REF defaults to HEAD, so running this script against a dirty working tree
# checks that the committer updated the JSON Schema when they changed values.yaml.
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
VALUES="helm/temporal-worker-controller/values.yaml"
SCHEMA="helm/temporal-worker-controller/values.schema.json"

GIT_REF="${GIT_REF:-$(git rev-parse --verify --quiet HEAD)}"
base="${GIT_REF}"

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
  local ref="$1"
  local path="$2"
  ! git diff --quiet "${ref}" -- "${path}"
}

cd "${ROOT}"

if [[ -z "${base}" ]] || ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
  echo "GIT_REF '${base}' is not a usable commit; set GIT_REF to a fetched ref" >&2
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
