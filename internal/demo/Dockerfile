# Build the manager binary
FROM golang:1.25 as builder
ARG TARGETOS
ARG TARGETARCH
ARG WORKER

# Choose the worker directory based on the build argument
WORKDIR /internal/demo
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY util util
# Copy everything in the worker directory recursively
# But skip any files named temporal_worker_deployment.yaml or Dockerfile or _test.go
COPY ${WORKER} ${WORKER}
RUN find ${WORKER} -name "temporal_worker.yaml" -prune -o -name "Dockerfile" -prune -o -name "*_test.go" -prune -o -type f -print

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o worker ./${WORKER}/cmd/worker

# Use distroless as minimal base image to package the worker binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM scratch
ARG DD_GIT_REPOSITORY_URL
ARG DD_GIT_COMMIT_SHA
ENV DD_GIT_REPOSITORY_URL=${DD_GIT_REPOSITORY_URL}
ENV DD_GIT_COMMIT_SHA=${DD_GIT_COMMIT_SHA}
ENV DD_SERVICE=${WORKER}-worker
WORKDIR /
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /internal/demo/worker .
USER 65532:65532

ENTRYPOINT ["/worker"]
