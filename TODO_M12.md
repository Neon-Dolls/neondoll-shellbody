# M12.1 Implementation Plan

## Step 1: Add Core 5 execution types to internal/link/core5/types.go
- Add execution request/result message types for Core 5
- Add capability advertisement types for Core 5

## Step 2: Create Core 5 execution adapter in internal/execution/core5.go
- Adapter maps canonical execution.request → process executor → canonical execution.result

## Step 3: Create Core 5 session handler in internal/body/core5session.go
- Handle Core 5 execution requests over authenticated Doll Link
- Use existing process executor

## Step 4: Update recovery loop to support Core 5 sessions
- Extend RunRecovery to handle Core 5 execution sessions

## Step 5: Create integration test harness for Core 5 execution
- Test full round trip: Doll → Core 5 → ToolExecutor → Body Resolver → Exec → result back

## Step 6: Fix M11.3 regression test
- Replace placeholder TestCloseSessionAfterDisconnectVerifiesSessionID with real test

## Step 7: Run quality gates
- gofmt -l .
- go vet ./...
- go build -race ./...
- go test -race -p 2 ./...

## Step 8: Open PR and report