#!/usr/bin/env python3
"""Make controller-owned DeploymentSpec.selector optional in the generated CRD."""

from pathlib import Path

crd = Path("helm/temporal-worker-controller-crds/templates/temporal.io_workerdeployments.yaml")
start_marker = "\n              deployment:\n"
end_marker = "\n              minReadySeconds:\n"
required = "                required:\n                - selector\n                - template\n"
optional = "                required:\n                - template\n"

schema = crd.read_text()
start = schema.index(start_marker)
end = schema.index(end_marker, start)
deployment = schema[start:end]
if deployment.count(required) != 1:
    raise SystemExit(f"ERROR: expected one required DeploymentSpec selector in {crd}")
crd.write_text(schema[:start] + deployment.replace(required, optional) + schema[end:])
