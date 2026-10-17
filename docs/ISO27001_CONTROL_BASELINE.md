# ISO/IEC 27001:2022 control baseline

**Purpose:** engineering control register for planning and evidence collection. This is not a Statement of Applicability, audit result, or certification claim. The ISMS scope, risk treatment, legal entity, control owners, risk appetite, and evidence retention period have not been supplied and remain decisions for the organization.

The control references below use Annex A of ISO/IEC 27001:2022. Applicability and implementation status must be decided through the organization's risk assessment and Statement of Applicability; a code repository cannot decide those on behalf of the organization. See [ISO/IEC 27001:2022](https://www.iso.org/standard/27001?a=4JDQV06G).

## Initial engineering register

| Control area | Repository evidence today | Gap to close | Owner / evidence |
|---|---|---|---|
| A.5.15–A.5.18 access, identity, authentication information, access rights | Seller routes and sales/inventory gRPC boundaries require live sessions and tenant-scoped grants; PAP writes are transactionally recorded and policy audit history is paginated behind `authz.audit.read`; employee roster read requires `authz.employee.read`; tenant assignment history and provenance are paginated behind `authz.assignment.review`; legacy unauthenticated employee CRUD RPCs are disabled | OD-5 bootstrap ceremony awaits ratification. Legacy assignments may lack a human actor. No employee lifecycle API or periodic access-review workflow/attestation evidence yet | Identity/security owner; ratify bootstrap procedure, define employee lifecycle API, and produce dated access-review evidence |
| A.5.23 cloud services | Kubernetes manifests and external platform service contracts | Cloud account ownership, provider assessment, region/residency, and shared-responsibility records are absent here | Platform/security owner; provider register and review |
| A.5.24–A.5.28 incident management and evidence | Operational runbooks exist for selected dependencies | No organization-approved incident process, severity criteria, contact tree, or evidence chain | Security/operations owner; tabletop and incident records |
| A.5.30 ICT readiness for business continuity | Deployment and health-check configuration exists | Recovery objectives, dependency recovery order, and restore exercise results are unknown | Service owners; approved RTO/RPO and exercise evidence |
| A.8.8 vulnerability management | Go dependency manifests and CI checks are present | No repository evidence of a complete vulnerability triage SLA, exception expiry, or image scan gate | Security/platform owner; scan reports and exception register |
| A.8.9 configuration management | Environment configuration and Kubernetes manifests are versioned | Production drift detection and approved configuration inventory are not evidenced | Platform owner; environment inventory and drift review |
| A.8.13 information backup | Database configuration is present | Backup schedule, encryption, retention, restore validation, and recovery evidence are unknown | Data/platform owner; restore exercise evidence |
| A.8.15–A.8.16 logging and monitoring | Structured logs, request metrics, readiness, dependency breakers, a durable inventory movement ledger, structured authorization-denial events at sales/inventory boundaries, and queryable transactional PAP audit events with command reasons exist; database triggers reject ordinary event update/delete/truncate operations | `internal/shared/foundation/audit` remains an in-memory, unused general adapter. Database owners can alter trigger/table controls; external immutable storage and verified retention are absent. Durable authorization-decision coverage and durable audit for identity, order, payment, and PII mutations, plus centralized integrity protection and review, are not implemented | Security/data owner; approve event scope, storage/access policy, retention, integrity controls and review evidence |
| A.8.24 use of cryptography | TLS-capable service clients and session cookie policy exist | Key lifecycle, rotation, encryption-at-rest decisions, and TLS policy evidence are incomplete | Security/platform owner; key inventory and rotation evidence |
| A.8.25–A.8.29 secure development | Architecture checks, code, and test targets exist | Secure design review, threat models, security test policy, and release evidence need an agreed owner and CI workflow | Engineering/security owner; threat models, CI artifacts, review records |
| A.8.32 change management | Git and generated contract checks support reviewable changes | Branch protection, approval rules, emergency change records, and production deployment approval are not evidenced | Engineering owner; repository settings export and deployment records |

## Required evidence work

1. Approve the ISMS scope and identify the systems, data classes, environments, and suppliers in scope.
2. Name control owners from the actual organization. Team catalog IDs are architecture placeholders and are not evidence of assigned people or accountability.
3. Complete risk assessment and treatment; determine applicability and document the rationale in the Statement of Applicability.
4. Classify and inventory personal, payment, seller, order, inventory, and operational data from actual data flows. Do not infer collection or retention practices from model names.
5. Define the security-relevant event scope and transaction boundaries; then provide durable, access-controlled audit records and define integrity, retention, and review controls before claiming audit coverage. Inventory movement records are not general audit coverage.
6. Exercise backup restore, incident response, credential rotation, and access review; retain dated evidence in the organization-approved evidence store.

## CMS-specific controls

- Builder.io is a public-content read source only. The public API key is configuration, not a secret, and must have read-only scope. Private write credentials must never be shipped to the gateway or browser.
- CMS editor identity and publishing roles remain managed by the CMS provider until a separately approved identity integration exists. Local merchant roles do not imply platform CMS permissions.
- Only published content using the versioned allowlist may render. Unknown block types, script-bearing fields, unsafe URL schemes, and oversized payloads must fail closed.
- Production publication of legal terms and privacy text is blocked until business and legal owners provide and approve the actual content.
