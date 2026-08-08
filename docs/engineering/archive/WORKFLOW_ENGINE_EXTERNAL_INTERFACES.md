> **Archived with the workflow-engine plan** it describes the interfaces for.
> See TODOS.md PS-008.

# Workflow Engine External Service Interface Specification

## Overview

The workflow engine must seamlessly integrate with external domain-specific services to execute real-world business processes. This specification defines how external services connect to and interact with the workflow engine.

## Core Concepts

### Service Connector
A service connector is a bidirectional interface between the workflow engine and external systems. Each connector:
- Maintains its own connection pool
- Handles authentication and authorization
- Manages retries and circuit breaking
- Propagates correlation IDs and traces
- Ensures idempotency

### Domain Service
An external system that provides domain-specific functionality:
- HR systems (Workday, BambooHR)
- Payment processors (Stripe, PayPal)
- Background check services (Sterling, Checkr)
- Tax services (Avalara, TaxJar)
- CRM systems (Salesforce, HubSpot)
- ERP systems (SAP, Oracle)
- Custom internal services

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                   Workflow Engine                        │
├─────────────────────────────────────────────────────────┤
│                  Service Interface Layer                 │
│  ┌────────────────────────────────────────────────────┐ │
│  │            Service Connector Framework             │ │
│  ├────────────────────────────────────────────────────┤ │
│  │ • Service Registry      • Connection Pooling       │ │
│  │ • Auth Management       • Circuit Breakers         │ │
│  │ • Retry Policies        • Timeout Management       │ │
│  │ • Idempotency          • Observability             │ │
│  └────────────────────────────────────────────────────┘ │
├─────────────────────────────────────────────────────────┤
│                     Adapters                             │
│  ┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐         │
│  │ gRPC │ │ REST │ │GraphQL│ │ MQ   │ │ SOAP │         │
│  └──────┘ └──────┘ └──────┘ └──────┘ └──────┘         │
└─────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────┐
│                  External Services                       │
├─────────────────────────────────────────────────────────┤
│  HR Systems │ Finance │ IT Ops │ Legal │ Custom Apps    │
└─────────────────────────────────────────────────────────┘
```

## Service Interface Definition

### gRPC Service Contract

```protobuf
syntax = "proto3";
package workflow.v1;

service ExternalTaskExecutor {
  // Execute a single task
  rpc ExecuteTask(TaskRequest) returns (TaskResponse);
  
  // Execute batch of similar tasks
  rpc ExecuteBatch(BatchTaskRequest) returns (BatchTaskResponse);
  
  // Stream tasks for long-running operations
  rpc StreamTask(stream TaskRequest) returns (stream TaskResponse);
  
  // Health check
  rpc HealthCheck(Empty) returns (HealthStatus);
  
  // Get service capabilities
  rpc GetCapabilities(Empty) returns (ServiceCapabilities);
}

message TaskRequest {
  string task_id = 1;
  string workflow_id = 2;
  string task_type = 3;
  string idempotency_key = 4;
  google.protobuf.Any payload = 5;
  map<string, string> metadata = 6;
  string correlation_id = 7;
  string trace_parent = 8;  // W3C trace context
  AuthContext auth = 9;
  int32 attempt_number = 10;
  google.protobuf.Timestamp deadline = 11;
}

message TaskResponse {
  string task_id = 1;
  TaskStatus status = 2;
  google.protobuf.Any result = 3;
  string error_message = 4;
  ErrorClass error_class = 5;
  map<string, string> metadata = 6;
  repeated SideEffect side_effects = 7;
  CostMetrics cost = 8;
  int32 retry_after_seconds = 9;
}

enum TaskStatus {
  PENDING = 0;
  IN_PROGRESS = 1;
  COMPLETED = 2;
  FAILED = 3;
  RETRY = 4;
  COMPENSATED = 5;
}

enum ErrorClass {
  NONE = 0;
  RETRYABLE = 1;
  PERMANENT = 2;
  RATE_LIMITED = 3;
  AUTH_FAILED = 4;
  VALIDATION = 5;
  TIMEOUT = 6;
}

message SideEffect {
  string effect_id = 1;
  string effect_type = 2;
  string description = 3;
  bool reversible = 4;
  string compensation_method = 5;
}
```

## Service Registry

### Service Definition
```yaml
services:
  - name: hr-workday
    type: grpc
    endpoint: workday.internal:443
    auth:
      type: oauth2
      client_id: ${WORKDAY_CLIENT_ID}
      client_secret: ${WORKDAY_CLIENT_SECRET}
      token_url: https://workday.com/oauth/token
    retry:
      max_attempts: 3
      backoff: exponential
      initial_delay: 1s
      max_delay: 30s
    circuit_breaker:
      failure_threshold: 5
      timeout: 60s
      half_open_attempts: 3
    timeout:
      connect: 5s
      request: 30s
    rate_limit:
      requests_per_second: 100
      burst: 200
    capabilities:
      - employee.create
      - employee.update
      - employee.terminate
      - payroll.setup
      - benefits.enroll

  - name: background-check-sterling
    type: rest
    endpoint: https://api.sterlingcheck.com/v2
    auth:
      type: api_key
      header: X-API-Key
      value: ${STERLING_API_KEY}
    retry:
      max_attempts: 5
      backoff: exponential
    timeout:
      request: 120s  # Background checks can be slow
    capabilities:
      - criminal.check
      - employment.verify
      - education.verify
      - reference.check

  - name: payment-stripe
    type: rest
    endpoint: https://api.stripe.com/v1
    auth:
      type: bearer
      token: ${STRIPE_SECRET_KEY}
    retry:
      max_attempts: 3
      idempotency_header: Idempotency-Key
    timeout:
      request: 30s
    capabilities:
      - payment.charge
      - payment.refund
      - customer.create
      - subscription.create
```

## Workflow DSL Integration

### External Task Definition
```json
{
  "workflow": {
    "name": "employee_onboarding",
    "version": "1.0.0",
    "states": {
      "create_hr_record": {
        "type": "external_task",
        "service": "hr-workday",
        "operation": "employee.create",
        "input_mapping": {
          "firstName": "$.candidate.first_name",
          "lastName": "$.candidate.last_name",
          "email": "$.candidate.email",
          "startDate": "$.offer.start_date",
          "salary": "$.offer.salary",
          "department": "$.offer.department"
        },
        "output_mapping": {
          "$.employee.id": "$.result.employeeId",
          "$.employee.workday_id": "$.result.workdayId"
        },
        "retry": {
          "max_attempts": 3,
          "backoff": "exponential"
        },
        "compensation": {
          "operation": "employee.delete",
          "input": {
            "employeeId": "$.employee.workday_id"
          }
        },
        "timeout": "5m",
        "on_success": "run_background_check",
        "on_failure": "notify_hr_failure"
      },
      
      "run_background_check": {
        "type": "parallel",
        "branches": [
          {
            "name": "criminal_check",
            "type": "external_task",
            "service": "background-check-sterling",
            "operation": "criminal.check",
            "async": true,
            "callback_timeout": "72h"
          },
          {
            "name": "employment_verification",
            "type": "external_task",
            "service": "background-check-sterling",
            "operation": "employment.verify",
            "async": true,
            "callback_timeout": "48h"
          }
        ],
        "join": "all",
        "on_complete": "setup_it_accounts"
      },
      
      "setup_it_accounts": {
        "type": "external_task",
        "service": "it-provisioning",
        "operation": "accounts.provision",
        "batch": {
          "enabled": true,
          "max_size": 50,
          "max_wait": "30s"
        }
      }
    }
  }
}
```

## Connector Implementation Pattern

### Base Connector Interface
```go
type ServiceConnector interface {
    // Execute a single task
    Execute(ctx context.Context, task Task) (Result, error)
    
    // Execute batch of tasks
    ExecuteBatch(ctx context.Context, tasks []Task) ([]Result, error)
    
    // Start async task
    StartAsync(ctx context.Context, task Task) (string, error)
    
    // Check async task status
    CheckStatus(ctx context.Context, operationID string) (Result, error)
    
    // Compensate a completed task
    Compensate(ctx context.Context, task Task, originalResult Result) error
    
    // Health check
    HealthCheck(ctx context.Context) error
    
    // Get service capabilities
    GetCapabilities() []string
}

type Task struct {
    ID             string
    WorkflowID     string
    Type           string
    Operation      string
    Input          map[string]interface{}
    IdempotencyKey string
    Metadata       map[string]string
    CorrelationID  string
    TraceContext   string
    AuthContext    AuthContext
    Deadline       time.Time
}

type Result struct {
    Status       TaskStatus
    Output       map[string]interface{}
    Error        error
    ErrorClass   ErrorClass
    SideEffects  []SideEffect
    Cost         CostMetrics
    RetryAfter   time.Duration
}
```

### gRPC Connector Example
```go
type GRPCConnector struct {
    client      ExternalTaskExecutorClient
    config      ServiceConfig
    pool        *ConnectionPool
    breaker     *CircuitBreaker
    rateLimiter *RateLimiter
    metrics     *Metrics
}

func (c *GRPCConnector) Execute(ctx context.Context, task Task) (Result, error) {
    // Check circuit breaker
    if !c.breaker.Allow() {
        return Result{}, ErrCircuitOpen
    }
    
    // Apply rate limiting
    if err := c.rateLimiter.Wait(ctx); err != nil {
        return Result{}, err
    }
    
    // Get connection from pool
    conn := c.pool.Get()
    defer c.pool.Put(conn)
    
    // Prepare request with all context
    req := &TaskRequest{
        TaskId:         task.ID,
        WorkflowId:     task.WorkflowID,
        TaskType:       task.Type,
        IdempotencyKey: task.IdempotencyKey,
        Payload:        marshalPayload(task.Input),
        Metadata:       task.Metadata,
        CorrelationId:  task.CorrelationID,
        TraceParent:    task.TraceContext,
        Auth:           convertAuth(task.AuthContext),
        Deadline:       timestamppb.New(task.Deadline),
    }
    
    // Execute with timeout and retries
    var resp *TaskResponse
    err := retry.Do(
        func() error {
            var err error
            resp, err = c.client.ExecuteTask(ctx, req)
            return err
        },
        retry.Attempts(c.config.Retry.MaxAttempts),
        retry.Delay(c.config.Retry.InitialDelay),
        retry.MaxDelay(c.config.Retry.MaxDelay),
        retry.RetryIf(isRetryable),
    )
    
    // Update circuit breaker
    if err != nil {
        c.breaker.RecordFailure()
        return Result{}, err
    }
    c.breaker.RecordSuccess()
    
    // Record metrics
    c.metrics.RecordLatency(time.Since(start))
    c.metrics.RecordRequest(task.Type, resp.Status)
    
    return convertResponse(resp), nil
}
```

## Batch Processing

### Batch Accumulator
```go
type BatchAccumulator struct {
    service     string
    operation   string
    maxSize     int
    maxWait     time.Duration
    tasks       []Task
    results     chan BatchResult
    mu          sync.Mutex
}

func (b *BatchAccumulator) Add(task Task) <-chan Result {
    b.mu.Lock()
    defer b.mu.Unlock()
    
    b.tasks = append(b.tasks, task)
    resultChan := make(chan Result, 1)
    
    if len(b.tasks) >= b.maxSize {
        go b.flush()
    } else if len(b.tasks) == 1 {
        go b.waitAndFlush()
    }
    
    return resultChan
}

func (b *BatchAccumulator) flush() {
    connector := GetConnector(b.service)
    results, err := connector.ExecuteBatch(context.Background(), b.tasks)
    
    for i, task := range b.tasks {
        b.results <- BatchResult{
            TaskID: task.ID,
            Result: results[i],
            Error:  err,
        }
    }
    
    b.tasks = nil
}
```

## Async Operations

### Callback Handler
```go
type CallbackHandler struct {
    registry *ServiceRegistry
    store    AsyncOperationStore
}

func (h *CallbackHandler) HandleCallback(
    ctx context.Context,
    serviceID string,
    operationID string,
    result interface{},
) error {
    // Retrieve original task context
    op, err := h.store.Get(operationID)
    if err != nil {
        return err
    }
    
    // Validate callback is expected
    if op.Status != StatusPending {
        return ErrUnexpectedCallback
    }
    
    // Convert result
    taskResult := h.convertResult(serviceID, result)
    
    // Emit completion event
    event := &TaskCompletedEvent{
        TaskID:     op.TaskID,
        WorkflowID: op.WorkflowID,
        Result:     taskResult,
        Timestamp:  time.Now(),
    }
    
    return h.eventStore.Append(event)
}
```

## Security Considerations

### Authentication Management
```go
type AuthManager struct {
    providers map[string]AuthProvider
    vault     SecretVault
    cache     TokenCache
}

type AuthProvider interface {
    Authenticate(ctx context.Context, config AuthConfig) (Token, error)
    Refresh(ctx context.Context, token Token) (Token, error)
}

type OAuth2Provider struct {
    client *oauth2.Client
}

func (p *OAuth2Provider) Authenticate(ctx context.Context, config AuthConfig) (Token, error) {
    // Get credentials from vault
    clientID := p.vault.Get(config.ClientIDKey)
    clientSecret := p.vault.Get(config.ClientSecretKey)
    
    // Request token
    token, err := p.client.RequestToken(
        clientID,
        clientSecret,
        config.TokenURL,
    )
    
    return Token{
        Value:   token.AccessToken,
        Expires: token.Expiry,
        Type:    "Bearer",
    }, err
}
```

## Monitoring & Observability

### Service Metrics
```go
type ServiceMetrics struct {
    RequestCount     *prometheus.CounterVec
    RequestDuration  *prometheus.HistogramVec
    ErrorRate        *prometheus.GaugeVec
    CircuitState     *prometheus.GaugeVec
    ConnectionPool   *prometheus.GaugeVec
    RateLimitDelay   *prometheus.HistogramVec
}

func (m *ServiceMetrics) RecordRequest(
    service string,
    operation string,
    status string,
    duration time.Duration,
) {
    m.RequestCount.WithLabelValues(service, operation, status).Inc()
    m.RequestDuration.WithLabelValues(service, operation).Observe(duration.Seconds())
    
    if status == "error" {
        m.ErrorRate.WithLabelValues(service, operation).Inc()
    }
}
```

### Distributed Tracing
```go
func (c *ServiceConnector) Execute(ctx context.Context, task Task) (Result, error) {
    // Start span
    ctx, span := tracer.Start(ctx, "external_service.execute",
        trace.WithAttributes(
            attribute.String("service.name", c.service),
            attribute.String("operation", task.Operation),
            attribute.String("task.id", task.ID),
            attribute.String("workflow.id", task.WorkflowID),
        ),
    )
    defer span.End()
    
    // Propagate trace context
    carrier := propagation.MapCarrier(task.Metadata)
    propagator.Inject(ctx, carrier)
    
    // Execute task
    result, err := c.doExecute(ctx, task)
    
    // Record result
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
    } else {
        span.SetStatus(codes.Ok, "")
    }
    
    return result, err
}
```

## Testing Support

### Mock Service Framework
```go
type MockService struct {
    responses map[string]Result
    delays    map[string]time.Duration
    errors    map[string]error
}

func (m *MockService) Execute(ctx context.Context, task Task) (Result, error) {
    key := fmt.Sprintf("%s:%s", task.Type, task.Operation)
    
    // Simulate delay
    if delay, ok := m.delays[key]; ok {
        time.Sleep(delay)
    }
    
    // Return error if configured
    if err, ok := m.errors[key]; ok {
        return Result{}, err
    }
    
    // Return configured response
    if result, ok := m.responses[key]; ok {
        return result, nil
    }
    
    return Result{Status: TaskStatusCompleted}, nil
}
```

## Migration & Versioning

### Service Version Management
```go
type VersionedService struct {
    versions map[string]ServiceConnector
    router   VersionRouter
}

func (v *VersionedService) Execute(ctx context.Context, task Task) (Result, error) {
    // Determine version to use
    version := v.router.Route(task)
    
    // Get appropriate connector
    connector, ok := v.versions[version]
    if !ok {
        return Result{}, ErrVersionNotSupported
    }
    
    // Execute with version-specific connector
    return connector.Execute(ctx, task)
}
```

## Error Handling

### Error Classification
```go
func ClassifyError(err error) ErrorClass {
    switch {
    case errors.Is(err, context.DeadlineExceeded):
        return ErrorClassTimeout
    case errors.Is(err, ErrRateLimited):
        return ErrorClassRateLimited
    case errors.Is(err, ErrAuthFailed):
        return ErrorClassAuthFailed
    case isRetryable(err):
        return ErrorClassRetryable
    default:
        return ErrorClassPermanent
    }
}

func HandleServiceError(task Task, err error) Decision {
    class := ClassifyError(err)
    
    switch class {
    case ErrorClassRetryable:
        return Decision{
            Action: ActionRetry,
            Delay:  calculateBackoff(task.AttemptNumber),
        }
    case ErrorClassRateLimited:
        return Decision{
            Action: ActionRetry,
            Delay:  extractRetryAfter(err),
        }
    case ErrorClassTimeout:
        if task.AttemptNumber < 3 {
            return Decision{Action: ActionRetry}
        }
        return Decision{Action: ActionFail}
    default:
        return Decision{Action: ActionFail}
    }
}
```

## Integration Examples

### Employee Onboarding
```go
func (w *Workflow) ExecuteOnboarding(ctx context.Context, candidate Candidate) error {
    // Create HR record
    hrResult, err := w.connectors["workday"].Execute(ctx, Task{
        Operation: "employee.create",
        Input: map[string]interface{}{
            "candidate": candidate,
            "startDate": candidate.StartDate,
        },
    })
    if err != nil {
        return err
    }
    
    // Parallel background checks
    var wg sync.WaitGroup
    checks := []string{"criminal", "employment", "education"}
    
    for _, check := range checks {
        wg.Add(1)
        go func(checkType string) {
            defer wg.Done()
            
            w.connectors["sterling"].Execute(ctx, Task{
                Operation: fmt.Sprintf("%s.check", checkType),
                Input: map[string]interface{}{
                    "employeeId": hrResult.Output["employeeId"],
                    "ssn":        candidate.SSN,
                },
            })
        }(check)
    }
    
    wg.Wait()
    
    // IT provisioning
    _, err = w.connectors["it-provisioning"].Execute(ctx, Task{
        Operation: "accounts.create",
        Input: map[string]interface{}{
            "employeeId": hrResult.Output["employeeId"],
            "email":      candidate.Email,
            "department": candidate.Department,
        },
    })
    
    return err
}
```

This external service interface specification provides a complete framework for integrating any external system with the workflow engine, ensuring reliability, observability, and maintainability.