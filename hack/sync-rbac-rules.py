#!/usr/bin/env python3
"""Sync rules from config/rbac/role.yaml into the Helm RBAC template.

The Helm template contains two marker pairs:

  # GENERATED RULES BEGIN           — full rules for the ClusterRole (default mode)
  # GENERATED RULES END

  # GENERATED RULES (NAMESPACED) BEGIN  — rules minus cluster-scoped entries for
  # GENERATED RULES (NAMESPACED) END      the namespaced Role (restrictWatchNamespaces)

Both are updated from config/rbac/role.yaml.  The namespaced block excludes
rules for cluster-scoped resources (namespaces) since those are handled by
the separate manager-cluster-role.
"""
import re
import sys

ROLE_YAML = "config/rbac/role.yaml"
HELM_RBAC = "helm/temporal-worker-controller/templates/rbac.yaml"

BEGIN_MARKER = "  # GENERATED RULES BEGIN"
END_MARKER = "  # GENERATED RULES END"
BEGIN_MARKER_NS = "  # GENERATED RULES (NAMESPACED) BEGIN"
END_MARKER_NS = "  # GENERATED RULES (NAMESPACED) END"

CLUSTER_SCOPED_RESOURCES = {
    "namespaces",
    "subjectaccessreviews",
    "clusterconnections",
    "clusterconnections/finalizers",
}


def extract_rules_text(path):
    """Extract the raw rules text from config/rbac/role.yaml."""
    with open(path) as f:
        content = f.read()
    idx = content.find("\nrules:\n")
    if idx == -1:
        print(f"ERROR: 'rules:' not found in {path}", file=sys.stderr)
        sys.exit(1)
    rules_body = content[idx + len("\nrules:\n"):]
    lines = rules_body.splitlines(keepends=True)
    result = []
    for line in lines:
        if not line.strip():
            result.append(line)
        elif line.startswith("  - "):
            result.append("    " + line)
        else:
            result.append("  " + line)
    return "".join(result)


def filter_namespaced(rules_text):
    """Remove rule blocks that reference cluster-scoped resources.

    Splits on top-level list items (lines starting with '  - ') and drops
    any block whose 'resources:' section contains a cluster-scoped resource.
    """
    blocks = []
    current = []
    for line in rules_text.splitlines(keepends=True):
        if line.startswith("  - ") and current:
            blocks.append("".join(current))
            current = [line]
        else:
            current.append(line)
    if current:
        blocks.append("".join(current))

    filtered = []
    for block in blocks:
        out_lines = []
        in_resources = False
        kept_resources = 0
        for line in block.splitlines(keepends=True):
            stripped = line.strip()
            if stripped == "resources:":
                in_resources = True
                out_lines.append(line)
                continue
            if in_resources and stripped.startswith("- "):
                resource = stripped[2:].strip()
                if resource in CLUSTER_SCOPED_RESOURCES:
                    continue
                kept_resources += 1
                out_lines.append(line)
                continue
            if in_resources and not stripped.startswith("- "):
                in_resources = False
            out_lines.append(line)
        if kept_resources > 0:
            filtered.append("".join(out_lines))

    return "".join(filtered)


def replace_between_markers(content, begin, end, replacement):
    pattern = re.compile(
        r"(" + re.escape(begin) + r"[^\n]*\n)(.*?)(" + re.escape(end) + r")",
        re.DOTALL,
    )
    if not pattern.search(content):
        print(f"ERROR: markers {begin!r} not found", file=sys.stderr)
        sys.exit(1)
    return pattern.sub(r"\g<1>" + replacement + r"\g<3>", content)


def main():
    rules_text = extract_rules_text(ROLE_YAML)

    with open(HELM_RBAC, "r+") as f:
        content = f.read()

        content = replace_between_markers(content, BEGIN_MARKER, END_MARKER, rules_text)
        content = replace_between_markers(
            content, BEGIN_MARKER_NS, END_MARKER_NS, filter_namespaced(rules_text)
        )

        f.seek(0)
        f.write(content)
        f.truncate()
    print(f"Synced RBAC rules from {ROLE_YAML} → {HELM_RBAC}")


if __name__ == "__main__":
    main()
