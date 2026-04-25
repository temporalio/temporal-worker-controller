# Releases

This document explains our thinking around Releases of Temporal Worker
Controller and associated Helm Charts.

A Release is a coordination point for publishing a versioned set of artifacts.
Each Release has a Github Release page which describes the changes in the
Release and the artifacts associated with it.

A Release Tag is simply the string name given to the Release.

In the Temporal Worker Controller project, we cut two different kinds of Releases:

* **Application Release**: contains the Temporal Worker Controller container image.
* **Chart Release**: contains the Helm Charts that install the Temporal Worker
  Controller and the Custom Resource Definitions (CRDs) used by the controller.

These different kinds of Releases are on different cadences and use different
version numbering.

## Application Releases

Application Releases refer to the Temporal Worker Controller code and the
`temporalio/temporal-worker-controller` container image artifacts.

Release Tags for official Application Releases will always use Semantic Version
strings, e.g. `v1.42.1`.

We bump the minor version number of the Application Release version when new
features are added **to the controller code itself**.

Likewise, We bump the patch version number of the Application Release version
when bug fixes are added to the controller code itself.

> **IMPORTANT**: Changes to the Application Release version string's minor and
> patch versions **do not imply a change to either the Helm Chart Release
> version or the APIVersion of the CRDs supported by the controller**. 

## Chart Releases

Chart Releases refer to the two Helm Charts that install the Temporal Worker
Controller and manage the Custom Resource Definitions (CRDs) used by the
controller.

Release Tags for official Chart Releases are prefixed with the string `helm-`
and the Semantic Version string for the Chart Release, e.g. `helm-v0.24.0`.

**The major version of the Semantic Version string for the Chart Release refers
to the APIVersion of the CRDs.**

While the APIVersion of the CRDs is on the `v1alpha` series, the major version
of the Semantic Version string for the Chart Release will remain on `v0`.

When the APIVersion of the CRDs moves to the `v1` series, the major version of
the Semantic Version string for the Chart Release will be bumped to `v1`.

The minor version of the Semantic Version string for the Chart Release is
bumped when we change the structure of the Helm Chart itself -- by adding,
removing or modifying `values.yaml` options or adding, removing or modifying
Kubernetes resources in the `templates/` directory.

The patch version of the Semantic Version string for the Chart Release is
bumped when we release a new Application Release and update the `Chart.yaml`'s
`appVersion` field to point to a new Application Release.

We will *always* cut a new Chart Release patch version for each new Application
Release. However, the Chart Release patch version may not be cut at the exact
same *time* as the Application Release. In this way, we can test the Helm Chart
release process in isolation from the Application release process.
