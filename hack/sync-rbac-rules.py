#!/usr/bin/env python3
"""Sync RBAC rules from controller-gen stdout into the Helm ClusterRole template.

The Helm template must contain:
  # GENERATED RULES BEGIN
  ...
  # GENERATED RULES END
markers inside its ClusterRole rules section.  Everything between the markers
is replaced with the rules emitted by controller-gen (indented by two spaces).

Usage: controller-gen ... output:rbac:stdout | python3 hack/sync-rbac-rules.py
"""
import re
import sys

HELM_RBAC = "helm/temporal-worker-controller/templates/rbac.yaml"
BEGIN_MARKER = "  # GENERATED RULES BEGIN"
END_MARKER = "  # GENERATED RULES END"


def extract_rules_text(content):
    idx = content.find("\nrules:\n")
    if idx == -1:
        print("ERROR: 'rules:' not found in controller-gen output", file=sys.stderr)
        sys.exit(1)
    rules_body = content[idx + len("\nrules:\n"):]
    # Indent lines relative to the `rules:` key in the Helm template.
    # controller-gen emits two indentation levels:
    #   col 0: outer list items  (e.g. "- apiGroups:")   → add 2 spaces
    #   col 2: inner list values (e.g. "  - events")     → add 4 spaces
    #   col 2: mapping keys      (e.g. "  resources:")   → add 2 spaces
    # The extra indent on inner list values matches the style used by the
    # hand-authored rules in the Helm template.
    lines = rules_body.splitlines(keepends=True)
    result = []
    for line in lines:
        if not line.strip():
            result.append(line)
        elif line.startswith("  - "):
            result.append("    " + line)  # inner list value: 2 global + 2 extra
        else:
            result.append("  " + line)    # outer list item or mapping key: 2 global
    return "".join(result)


def update_helm(path, rules_text):
    with open(path) as f:
        content = f.read()
    pattern = re.compile(
        r"(" + re.escape(BEGIN_MARKER) + r"[^\n]*\n)(.*?)(" + re.escape(END_MARKER) + r")",
        re.DOTALL,
    )
    if not pattern.search(content):
        print(f"ERROR: markers not found in {path}", file=sys.stderr)
        sys.exit(1)
    updated = pattern.sub(r"\g<1>" + rules_text + r"\g<3>", content)
    with open(path, "w") as f:
        f.write(updated)
    print(f"Synced RBAC rules into {HELM_RBAC}")


if __name__ == "__main__":
    content = sys.stdin.read()
    rules = extract_rules_text(content)
    update_helm(HELM_RBAC, rules)
