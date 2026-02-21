# Limits

- Max `TemporalWorkerDeployment.Name` length: 63 characters, since Kubernetes label values are limited to 63 characters
- Max `BuildID` length: 63 characters, since Kubernetes label values are limited to 63 characters

### Note on naming of controller-generated resources:
The controller generates a Kubernetes Deployment resource for each Version in your Worker Deployment.

Kubernetes operational best practice is to limit Deployment names to 47 characters to ensure that full Pod names are always less than 63 characters.

The controller will first generate the Deployment name for each Version as follows: `<TemporalWorkerDeployment.Name>-<BuildID>`.
If that name exceeds 47 characters, the controller will generate a unique less-than-47-character name for the Deployment.
You can find the names of all the Deployment resources owned by a certain `TemporalWorkerDeployment` in the `Status` field of the `TemporalWorkerDeployment` resource.

For transparency, know that the controller will generate these unique short names as follows, but don't depend on this name format because it may change: `trunc(<TemporalWorkerDeployment.Name>)-trunc(<BuildID>)-hash(<TemporalWorkerDeployment.Name>-<BuildID>)`