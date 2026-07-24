#!/usr/bin/env python3
"""Sync rules from config/rbac/role.yaml into the Helm RBAC template.

The Helm template contains two marker pairs:

  # GENERATED RULES BEGIN           — full rules for the ClusterRole (default mode)
  # GENERATED RULES END

  # GENERATED RULES (NAMESPACED) BEGIN  — rules minus cluster-scoped entries for
  # GENERATED RULES (NAMESPACED) END      the namespaced Role (restrictWatchNamespaces)

Both are updated from config/rbac/role.yaml.  The namespaced block excludes
rules for cluster-scoped resources (namespaces, subjectaccessreviews) since
those are handled by the separate manager-cluster-role.
"""
import re
import sys

import yaml

ROLE_YAML = "config/rbac/role.yaml"
HELM_RBAC = "helm/temporal-worker-controller/templates/rbac.yaml"

BEGIN_MARKER = "  # GENERATED RULES BEGIN"
END_MARKER = "  # GENERATED RULES END"
BEGIN_MARKER_NS = "  # GENERATED RULES (NAMESPACED) BEGIN"
END_MARKER_NS = "  # GENERATED RULES (NAMESPACED) END"

CLUSTER_SCOPED_RESOURCES = {"namespaces", "subjectaccessreviews"}


def extract_rules(path):
    """Return the list of rule dicts from config/rbac/role.yaml."""
    with open(path) as f:
        content = f.read()
    docs = list(yaml.safe_load_all(content))
    for doc in docs:
        if doc and doc.get("kind") == "ClusterRole" and "rules" in doc:
            return doc["rules"]
    print(f"ERROR: ClusterRole with rules not found in {path}", file=sys.stderr)
    sys.exit(1)


def rules_to_text(rules):
    """Convert a list of rule dicts to indented YAML text for the Helm template."""
    lines = []
    for rule in rules:
        lines.append("  - apiGroups:")
        for ag in rule.get("apiGroups", []):
            lines.append(f'      - "{ag}"' if ag == "" else f"      - {ag}")
        lines.append("    resources:")
        for res in rule.get("resources", []):
            lines.append(f"      - {res}")
        if "resourceNames" in rule:
            lines.append("    resourceNames:")
            for rn in rule["resourceNames"]:
                lines.append(f"      - {rn}")
        lines.append("    verbs:")
        for verb in rule.get("verbs", []):
            lines.append(f"      - {verb}")
    return "\n".join(lines) + "\n"


def filter_namespaced(rules):
    """Remove rules that reference cluster-scoped resources."""
    return [
        r for r in rules
        if not CLUSTER_SCOPED_RESOURCES.intersection(r.get("resources", []))
    ]


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
    rules = extract_rules(ROLE_YAML)

    with open(HELM_RBAC) as f:
        content = f.read()

    content = replace_between_markers(content, BEGIN_MARKER, END_MARKER, rules_to_text(rules))
    content = replace_between_markers(
        content, BEGIN_MARKER_NS, END_MARKER_NS, rules_to_text(filter_namespaced(rules))
    )

    with open(HELM_RBAC, "w") as f:
        f.write(content)
    print(f"Synced RBAC rules from {ROLE_YAML} → {HELM_RBAC}")


if __name__ == "__main__":
    main()
