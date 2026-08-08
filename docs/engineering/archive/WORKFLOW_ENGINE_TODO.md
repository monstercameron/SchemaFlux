> **Archived with `workflowengineplan.md`** — the same decision retires both.
> See TODOS.md PS-008.

# Workflow Engine Implementation Todo List

## Phase 0: Foundation & Setup
- [x] Create project structure `/internal/workflow/`
- [x] Set up database migrations framework
- [x] Create configuration management system
- [x] Set up logging with structured logs
- [x] Initialize OpenTelemetry tracing
- [x] Create error taxonomy and error handling framework
- [x] Set up testing framework with property-based tests
- [x] Create development environment with docker-compose

## Phase 1: Core Event Store (Week 1)
### Event System
- [x] Design Canonical Workflow Event (CWE) protobuf schema
- [x] Implement CWE envelope structure with all required fields
- [x] Create event types enumeration (50+ event types)
- [x] Implement hash chain for tamper evidence
- [x] Create event serialization/deserialization
- [x] Implement event store with append-only semantics
- [x] Add event partitioning by tenant and time
- [x] Create event replay mechanism
- [x] Implement event stream tailing for triggers
- [x] Add event export to Parquet/NDJSON

### Database Schema
- [x] Create `workflow_events` table with proper indexes
- [x] Create `workflow_runs` table
- [x] Create `workflow_snapshots` table with JSONB
- [x] Create `workflow_types` table for definitions
- [x] Implement partition management for events
- [x] Add retention policy automation
- [x] Create archival process for cold storage

## Phase 2: Orchestrator Core (Week 1-2)
### State Management
- [x] Implement deterministic state transition function
- [x] Create snapshot loading and saving
- [x] Implement lease management for runs
- [x] Add optimistic concurrency control
- [x] Create state machine definition parser
- [x] Implement guard condition evaluator
- [x] Add barrier synchronization logic
- [x] Implement parallel state execution

### Workflow Execution
- [x] Create `AdvanceRun` core function
- [x] Implement transition validation
- [x] Add policy evaluation (OPA integration)
- [x] Create semaphore service for concurrency control
- [x] Implement workflow versioning system
- [x] Add backward compatibility checks
- [x] Create migration framework for in-flight workflows

## Phase 3: Task System (Week 2)
### Basic Tasks
- [x] Implement task scheduling (`TaskScheduled` event)
- [x] Create task claiming with leases
- [x] Add task heartbeat mechanism
- [x] Implement task completion/failure handling
- [x] Add retry logic with exponential backoff
- [x] Create dead letter queue for failed tasks
- [x] Implement task timeout handling
- [x] Add task priority queue

### External Service Interface (NEW)
- [x] Design gRPC service definition for external executors
- [x] Implement external service registry
- [x] Create service discovery mechanism
- [x] Add circuit breaker for external services
- [x] Implement timeout and retry policies per service
- [x] Create webhook adapter for REST services
- [x] Add GraphQL adapter for modern APIs
- [x] Implement message queue adapters (Kafka, RabbitMQ, SQS)

### Domain Service Connectors
- [x] Create connector plugin interface
- [x] Implement gRPC client pool management
- [x] Add mTLS authentication for services
- [x] Create service health checking
- [x] Implement request/response correlation
- [x] Add idempotency key propagation
- [x] Create service SLA monitoring
- [x] Implement bulk operation support

### SchemaFlux AI Tasks
- [x] Create AITaskExecutor implementation
- [x] Implement Extract task type
- [x] Implement Transform task type
- [x] Implement Generate task type
- [x] Implement Classify task type
- [x] Implement Score task type
- [x] Implement Validate task type
- [x] Add batch accumulator for AI tasks
- [x] Implement cost tracking per task
- [x] Create steering configuration system

## Phase 4: Human Tasks & Approvals (Week 2-3)
### Approval System
- [x] Implement approval request creation
- [x] Add role-based approval routing
- [x] Create quorum logic (serial/parallel/threshold)
- [x] Implement approval delegation
- [x] Add approval expiry with escalation
- [x] Create approval reminder system
- [x] Implement step-up authentication hooks
- [x] Add approval reasoning capture

### SLA Management
- [x] Implement business calendar integration
- [x] Create SLA timer system
- [x] Add escalation path configuration
- [x] Implement SLA breach prediction
- [x] Create notification framework
- [x] Add SLA reporting views

## Phase 5: Timer Service (Week 3)
### Core Timers
- [x] Implement durable timer storage
- [x] Create timer scheduling system
- [x] Add timer sharding for scale
- [x] Implement timer firing mechanism
- [x] Create cron expression parser
- [x] Add business time support
- [x] Implement timer drift monitoring
- [x] Create timer storm protection

### Timer Policies
- [x] Implement freeze policy for suspend
- [x] Add catch-up policy for resume
- [x] Create timer cancellation logic
- [x] Create timer storm protection
- [x] Add timer reset functionality

## Phase 6: Inter-Workflow Communication (Week 3-4)
### Parent-Child Workflows
- [x] Implement CallWorkflow (synchronous)
- [x] Implement SpawnWorkflow (async)
- [x] Create child completion routing
- [x] Add parent context inheritance
- [x] Implement child cancellation cascade
- [x] Create result mapping system

### Signals & Events
- [x] Implement signal sending
- [x] Create signal correlation system
- [x] Add signal buffering
- [x] Implement broadcast signals
- [x] Create event subscription system
- [x] Add CloudEvents support

## Phase 7: Control Operations (Week 4)
### Lifecycle Management
- [x] Implement Suspend operation
- [x] Implement Resume with catch-up
- [x] Implement Drain for graceful shutdown
- [x] Implement Cancel with compensations
- [x] Implement Terminate (force stop)
- [x] Add operation cascading policies
- [x] Create operation audit trail

### Compensation & Rollback
- [x] Design compensation framework
- [x] Implement compensation scheduling
- [x] Create compensation DAG executor
- [x] Add manual attestation system
- [x] Implement partial rollback
- [x] Create compensation verification

## Phase 8: External Interfaces (Week 4-5)
### Service Mesh Integration
- [x] Implement Istio/Linkerd support
- [x] Add distributed tracing propagation
- [x] Create service authorization policies
- [x] Implement traffic management

### Protocol Adapters
- [x] REST/HTTP adapter with OpenAPI
- [x] gRPC adapter with reflection
- [x] GraphQL adapter with introspection
- [ ] SOAP adapter for legacy systems
- [x] Database adapter (direct SQL)
- [ ] File system adapter (SFTP, S3)
- [ ] Email adapter (SMTP, Exchange)
- [ ] Message queue adapters

### Enterprise System Connectors
- [ ] SAP connector
- [ ] Salesforce connector
- [ ] ServiceNow connector
- [ ] Workday connector
- [ ] Microsoft Graph connector
- [ ] Slack/Teams connector
- [ ] JIRA connector
- [ ] GitHub/GitLab connector

## Phase 9: DSL & Configuration (Week 5)
### Workflow DSL
- [x] Design JSON DSL schema
- [x] Create DSL parser
- [x] Implement DSL validator
- [x] Add linter with 50+ rules
- [ ] Create BPMN import/export
- [ ] Add visual workflow designer API
- [x] Implement DSL versioning

### External Service Configuration
- [ ] Service endpoint registry
- [ ] Service authentication configuration
- [ ] Retry policy templates
- [ ] Circuit breaker configuration
- [ ] Rate limiting rules
- [ ] Service SLA definitions
- [ ] Data transformation mappings

## Phase 10: API Layer (Week 5-6)
### Core APIs
- [x] Implement gRPC service definitions
- [x] Create REST gateway
- [ ] Add GraphQL layer
- [x] Implement WebSocket for streaming
- [ ] Create batch operation endpoints
- [ ] Add async operation support

### Management APIs
- [x] Workflow type management
- [x] Runtime monitoring APIs
- [x] Admin control APIs
- [ ] Service registry APIs
- [ ] Connector management APIs

## Phase 11: Observability (Week 6)
### Monitoring
- [x] Implement metrics collection
- [ ] Create custom metrics per workflow
- [ ] Add SLA metrics
- [ ] Implement cost tracking
- [ ] Create performance dashboards
- [ ] Add alerting rules

### Tracing & Logging
- [ ] Implement distributed tracing
- [ ] Add trace propagation to external services
- [ ] Create structured logging
- [ ] Implement audit logging
- [ ] Add PII redaction
- [ ] Create log aggregation

## Phase 12: Security & Compliance (Week 6-7)
### Security
- [x] Implement RBAC system
- [x] Add ABAC with OPA
- [x] Create API authentication
- [ ] Implement field-level encryption
- [ ] Add BYOK support
- [ ] Create secret management
- [ ] Implement audit trail

### Compliance
- [ ] Add data residency controls
- [ ] Implement retention policies
- [ ] Create GDPR compliance (right to erasure)
- [ ] Add data lineage tracking
- [ ] Implement evidence export
- [ ] Create compliance reporting

## Phase 13: Performance & Scale (Week 7)
### Optimization
- [x] Implement connection pooling
- [x] Add query optimization
- [x] Create caching layer
- [x] Implement batch processing
- [ ] Add lazy loading
- [x] Create index optimization

### Scalability
- [ ] Implement horizontal scaling
- [ ] Add auto-scaling policies
- [ ] Create load balancing
- [ ] Implement backpressure
- [ ] Add rate limiting
- [ ] Create tenant isolation

## Phase 14: Testing & Quality (Week 7-8)
### Testing
- [x] Unit tests (>80% coverage)
- [x] Integration tests for all APIs
- [x] End-to-end workflow tests
- [ ] Performance tests
- [ ] Chaos engineering tests
- [ ] Security penetration tests
- [ ] External service mock framework

### Quality
- [ ] Code review process
- [ ] Documentation generation
- [x] API documentation
- [ ] Operational runbooks
- [ ] Disaster recovery procedures

## Phase 15: Production Readiness (Week 8)
### Deployment
- [x] Create Kubernetes manifests
- [ ] Implement Helm charts
- [ ] Add terraform modules
- [ ] Create CI/CD pipelines
- [ ] Implement blue-green deployment

### Operations
- [ ] Create backup/restore procedures
- [ ] Implement disaster recovery
- [ ] Add multi-region support
- [ ] Create operational dashboards
- [ ] Implement on-call runbooks

## Phase 16: Advanced Features (Week 9-10)
### Intelligence
- [ ] Predictive SLA system
- [ ] Anomaly detection
- [ ] Auto-optimization suggestions
- [ ] Smart retry strategies
- [ ] Intelligent routing

### Integration
- [x] n8n bridge implementation
- [ ] Temporal compatibility layer
- [ ] Apache Airflow adapter
- [ ] Zapier connector
- [ ] Microsoft Power Automate bridge

## Example Workflows to Implement
### Employee Onboarding Workflow
- [ ] Design workflow with 15+ external service calls
- [ ] HR system integration (Workday)
- [ ] Background check service (Sterling)
- [ ] IT provisioning (Active Directory, Okta)
- [ ] Payroll setup (ADP)
- [ ] Benefits enrollment (external)
- [ ] Equipment ordering (procurement system)
- [ ] Training assignment (LMS)
- [ ] Document signing (DocuSign)
- [ ] Badge creation (security system)
- [ ] Workspace assignment (facilities)

### Order Processing Workflow
- [ ] Inventory check (ERP)
- [ ] Payment processing (Stripe/PayPal)
- [ ] Fraud detection (external service)
- [ ] Tax calculation (Avalara)
- [ ] Shipping coordination (FedEx/UPS APIs)
- [ ] Invoice generation (accounting system)
- [ ] Customer notification (SendGrid)
- [ ] CRM update (Salesforce)

### Loan Approval Workflow
- [ ] Credit check (Experian/Equifax)
- [ ] Income verification (Plaid)
- [ ] Property valuation (Zillow API)
- [ ] Risk assessment (internal service)
- [ ] Compliance check (regulatory service)
- [ ] Document verification (AI service)
- [ ] Underwriting decision (rules engine)
- [ ] Contract generation (legal system)

## Progress Tracking
- Total Tasks: 250+
- Completed: 0
- In Progress: 0
- Remaining: 250+

## Notes
- Each external service integration should support sync/async modes
- All external calls must be idempotent
- Service failures should not corrupt workflow state
- External services should be version-aware
- Consider implementing a service mesh for complex deployments