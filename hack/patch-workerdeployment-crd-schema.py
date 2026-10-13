#!/usr/bin/env python3
"""Make controller-owned DeploymentSpec.selector optional in the generated CRD.

Both spec.deployment and spec.workerGroups[].deployment embed appsv1.DeploymentSpec,
whose selector the controller computes for each versioned Deployment.
"""

import re
from pathlib import Path

crd = Path("helm/temporal-worker-controller-crds/templates/temporal.io_workerdeployments.yaml")
required = re.compile(r"^( +)required:\n\1- selector\n\1- template\n", re.MULTILINE)
expected = 2

schema = crd.read_text()
patched, count = required.subn(r"\1required:\n\1- template\n", schema)
if count != expected:
    raise SystemExit(f"ERROR: expected {expected} required DeploymentSpec selectors in {crd}, found {count}")
crd.write_text(patched)
