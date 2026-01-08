# Load Test CLI Tool

A Go-based CLI tool for programmatically starting load test workflows for HPA testing.

## Building

```bash
cd internal/demo/scripts/loadtest
go build -o loadtest .
```

Or from the demo directory:

```bash
cd internal/demo
go build -o scripts/loadtest/loadtest ./scripts/loadtest
```

## Usage

### Basic Usage

```bash
# CPU load test (10 workflows, default settings)
./loadtest -cpu 3 -count 10

# Memory load test
./loadtest -memory 512 -cpu 0 -count 15

# Mixed load test
./loadtest -cpu 3 -memory 256 -count 20

# Custom duration
./loadtest -cpu 2 -memory 128 -duration 600 -count 5
```

### All Options

```bash
./loadtest [options]

Options:
  -address string
        Temporal server address (default from TEMPORAL_ADDRESS or "localhost:7233")
  -namespace string
        Temporal namespace (default from TEMPORAL_NAMESPACE or "default")
  -task-queue string
        Task queue name (default from TASK_QUEUE or "hello-world")
  -count int
        Number of workflows to start (default 10)
  -duration int
        Duration in seconds for each workflow (default 300)
  -cpu int
        CPU intensity: 0=none, 1=light, 2=medium, 3=heavy (default 2)
  -memory int
        Memory to allocate in MB (default 256)
  -heartbeat
        Enable activity heartbeats (default true)
  -workflow-id-prefix string
        Workflow ID prefix (default "load-test-{timestamp}")
  -stagger duration
        Delay between starting workflows (default 100ms)
```

### Environment Variables

The tool respects these environment variables:

- `TEMPORAL_ADDRESS`: Temporal server address
- `TEMPORAL_NAMESPACE`: Temporal namespace
- `TASK_QUEUE`: Task queue name

```bash
export TEMPORAL_ADDRESS="your-namespace.tmprl.cloud:7233"
export TEMPORAL_NAMESPACE="your-namespace"
export TASK_QUEUE="hello-world"

./loadtest -cpu 3 -count 20
```

## Examples

### Example 1: Quick CPU Spike

```bash
# 50 workflows, heavy CPU, 60 seconds each
./loadtest -cpu 3 -memory 0 -duration 60 -count 50 -stagger 50ms
```

### Example 2: Sustained Memory Pressure

```bash
# 20 workflows, 1GB memory each, 10 minutes
./loadtest -cpu 0 -memory 1024 -duration 600 -count 20
```

### Example 3: Gradual Load Increase

```bash
# Start with light load
./loadtest -cpu 1 -count 5 -workflow-id-prefix "load-wave-1"
sleep 60

# Increase load
./loadtest -cpu 2 -count 10 -workflow-id-prefix "load-wave-2"
sleep 60

# Heavy load
./loadtest -cpu 3 -count 20 -workflow-id-prefix "load-wave-3"
```

### Example 4: Long-Running Load Test

```bash
# 30 minute test with 100 workflows
./loadtest -cpu 2 -memory 256 -duration 1800 -count 100 -stagger 200ms
```

## Integration with CI/CD

This tool is designed for automation. Example usage in a CI/CD pipeline:

```yaml
# GitHub Actions example
- name: Run HPA Load Test
  run: |
    cd internal/demo/scripts/loadtest
    go run . \
      -address ${{ secrets.TEMPORAL_ADDRESS }} \
      -namespace ${{ secrets.TEMPORAL_NAMESPACE }} \
      -cpu 3 \
      -memory 256 \
      -count 50 \
      -duration 300
    
    # Wait for HPA to scale
    sleep 120
    
    # Verify scaling occurred
    kubectl get hpa -o json | jq '.status.currentReplicas'
```

## Advantages Over Shell Script

- **Cross-platform**: Works on Windows, Linux, macOS
- **Type-safe**: Uses Go structs for workflow parameters
- **Programmatic**: Easy to integrate with other Go code
- **Error handling**: Better error reporting and handling
- **No dependencies**: Single binary, no need for `temporal` CLI
- **Fast**: Efficient concurrent workflow starting

## Output Example

```
🚀 Starting 10 load test workflows
Configuration:
  Temporal Address: localhost:7233
  Namespace: default
  Task Queue: hello-world
  Duration: 300s
  CPU Intensity: 3
  Memory: 256MB
  Heartbeat: true
  Workflow ID Prefix: load-test-1704672000

✅ Started workflow 1/10
   Workflow ID: load-test-1704672000-1
   Run ID: a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d
✅ Started workflow 2/10
   Workflow ID: load-test-1704672000-2
   Run ID: b2c3d4e5-f6a7-4b8c-9d0e-1f2a3b4c5d6e
...

✨ Successfully started 10/10 workflows

📊 Monitoring Commands:
  kubectl get hpa -w
  kubectl top pods
  temporal workflow list --namespace default --query 'WorkflowType="LoadTestWorkflow"'
```

## Building for Different Platforms

```bash
# Linux
GOOS=linux GOARCH=amd64 go build -o loadtest-linux .

# macOS Intel
GOOS=darwin GOARCH=amd64 go build -o loadtest-darwin-amd64 .

# macOS Apple Silicon
GOOS=darwin GOARCH=arm64 go build -o loadtest-darwin-arm64 .

# Windows
GOOS=windows GOARCH=amd64 go build -o loadtest.exe .
```

## Troubleshooting

### Connection Failed

```
Failed to start workflow: connection refused
```

**Solution**: Check `TEMPORAL_ADDRESS` and ensure Temporal server is running.

### Task Queue Not Found

```
Failed to start workflow: workflow task not acknowledged
```

**Solution**: Ensure workers are running and listening to the correct task queue.

### Module Import Issues

```
cannot find module providing package github.com/temporalio/temporal-worker-controller/internal/demo/helloworld
```

**Solution**: Run `go mod tidy` to download dependencies.

## See Also

- Shell script version: `../loadtest.sh`
- Full documentation: `../../LOADTEST.md`
- Quick start guide: `../../LOADTEST_QUICKSTART.md`

