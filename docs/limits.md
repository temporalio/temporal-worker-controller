# Limits

- Max `WorkerDeployment.Name` length: 63 characters, since Kubernetes label values are limited to 63 characters
- Max `BuildID` length: 63 characters, since Kubernetes label values are limited to 63 characters

### Note on naming of controller-generated resources:
The controller generates a Kubernetes Deployment resource for each Version in your Worker Deployment.

Kubernetes operational best practice is to limit Deployment names to 47 characters to ensure that full Pod names are always less than 63 characters.

The controller will first generate the Deployment name for each Version as follows: `<WorkerDeployment.Name>-<BuildID>`.
If that name exceeds 47 characters, the controller will generate a unique less-than-47-character name for the Deployment.
You can find the names of all the Deployment resources owned by a certain `WorkerDeployment` in the `Status` field of the `WorkerDeployment` resource.
Note that the original untruncated deployment name and build ID can always be found in the `temporal.io/deployment-name` and `temporal.io/build-id` labels.

For transparency, know that the controller will generate these unique short names as follows, but don't depend on this name format because it may change: `trunc(<WorkerDeployment.Name>)-trunc(<BuildID>)-hash(<WorkerDeployment.Name>-<BuildID>)`