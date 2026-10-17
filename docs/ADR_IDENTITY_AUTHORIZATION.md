# ADR: Identity and authorization for gogo

**Status:** accepted architecture baseline; implementation is staged.  
**Date:** 2026-10-16  
**Scope:** the existing employee and seller-console identity surface. Shopper
identity, external federation, and platform-operator identity are not modeled by
the current auth module and are not implicitly covered by this decision.

## Context

The auth module currently authenticates employees and stores one `role` string
on each employee. That string is not an authorization model: it cannot represent
multiple roles, define permissions, express tenant scope, or check access to a
specific resource. The seller console now validates sessions, but this is
authentication only. A valid session must not be treated as blanket permission.

The system is a Go modular monolith with tenant context, application services,
Postgres adapters, and contract-backed gRPC module boundaries. The authorization
design should preserve those seams and avoid a second policy language until the
domain needs one.

## Decision

Use a **tenant-scoped, multi-role RBAC core with ABAC guards**, separated through
logical PEP/PDP/PIP/PAP interfaces:

| Component | Project responsibility |
|---|---|
| PEP — Policy Enforcement Point | Gateway/page checks for navigation and early rejection; each mutating module's application service checks access again for its operation and resource. The gateway is not the final authority. |
| PDP — Policy Decision Point | Auth/authorization application service makes an explicit allow/deny decision. Unknown identity, permission, tenant, resource, or evaluator failure is deny. |
| PIP — Policy Information Point | Auth/authorization adapters load active identity, tenant membership, role assignments, resource attributes, and domain relationships from their owning modules. |
| PAP — Policy Administration Point | A controlled admin capability creates tenant roles, maps known permissions to roles, and assigns/revokes roles for users. Its own access is protected by a separately provisioned bootstrap authority. |

### Permission model

- A user may hold **zero or more active roles** in a tenant. Assignments are
  separate records with assigner, start time, optional expiry, and revocation
  state; no role is embedded as an enduring authorization claim in the browser
  session.
- Roles and role-to-permission grants are tenant-scoped metadata. Role names are
  display/management data, not special code branches. The strings `admin`,
  `seller`, and `viewer` acquire no authority just by existing.
- Permission identifiers describe capabilities the application actually
  exposes. Their vocabulary and enforcement points are code-owned contracts;
  administrators can compose those capabilities into roles without deploying
  code. Unknown permission identifiers cannot grant access.
- Start with **grant-only RBAC** and deny by default. Explicit deny rules and
  precedence are not needed until the product has a concrete requirement.
- ABAC is used for invariant conditions that apply to every role: active
  identity, valid session, tenant equality, and operation-specific resource
  state. Domain invariants remain enforced by the owning application/domain
  service even when authorization allows the operation.
- Add ReBAC through the PIP when a real relationship such as employee-to-store
  membership, delegated seller access, or resource ownership is modeled by its
  owning domain. Do not create a generic relationship graph before those
  relationships have authoritative data and lifecycle rules.

The initial code-owned permission catalog is:

| Permission key | Capability |
|---|---|
| `authz.role.manage` | Create tenant roles |
| `authz.permission.manage` | Grant or revoke known permission keys |
| `authz.assignment.manage` | Assign or revoke tenant roles |
| `seller.dashboard.read` | View the seller dashboard |
| `seller.catalog.read` / `seller.catalog.manage` | Read or manage products |
| `seller.category.read` / `seller.category.manage` | Read or create merchant categories |
| `seller.inventory.read` / `seller.inventory.manage` | Read or adjust inventory |
| `seller.sales.read` / `seller.sales.checkout` / `seller.sales.void` | Read transactions, checkout, or void |

Permission keys are not created by PAP users. Adding a capability requires a
code-owned permission definition and enforcement point.

### Evaluation and trust

- Authorization is evaluated against current stored assignments, so role
  changes and revocations apply without waiting for a browser token to expire.
- The session identifies the subject; a caller-supplied role list, employee ID,
  tenant, actor name, or resource owner is never sufficient proof of authority.
- Tenant context is checked again by the application service and in the
  repository query/constraint. A decision for one tenant cannot be reused for
  another tenant.
- Service-to-service calls carry a verified principal context at the contract
  boundary. A gateway-only decision is insufficient for a module mutation.
- Decision and administrative-change records must eventually be durable and
  correlated with the business operation. The existing generic audit package is
  not that control: it is currently in-memory and is not transactionally coupled
  to writes.

## Why this fits this project

RBAC covers tenant employees and composable responsibilities with low runtime
and operational cost. The current code has no authoritative store relationship
graph or attribute policy language, so a full ReBAC/OPA/Casbin policy DSL would
introduce administration and debugging concepts before there is data to drive
them. The PDP interface keeps a later engine replaceable; role data, tenant
invariants, and enforcement points remain explicit.

The authorization capability belongs with the existing Identity/Auth stream
for this baseline: the same module owns employee lifecycle and session
validation. The code should keep authentication and authorization as separate
packages and application ports, so a later team or deployment split does not
require changing seller business modules.

## Persistence shape

The migration target is normalized and tenant-scoped:

```text
employees ──< employee_role_assignments >── roles
roles ──< role_permissions >── permission_catalog
```

`permission_catalog` describes supported resource/action pairs; it does not
contain business rules. `role_permissions` and `employee_role_assignments` are
editable policy metadata. Assignments include actor and time bounds for expiry,
JIT access, and review evidence. Resource relationships remain owned by their
domain and are queried through PIP ports.

The current `employees.role` field is legacy compatibility data. A migration
must preserve each existing value as a tenant-local role assignment before the
field is retired. It must not invent permission grants for those roles. Until a
role receives explicit grants, the PDP returns deny.

## Delivery sequence

### Delegated service operations (decision for the current modular monolith)

Sales-to-inventory stock effects are delegated operations. Inventory must receive
both the authenticated employee subject and the calling module identity, then
evaluate the requested operation against the subject's current permission. The
actor recorded for audit remains the employee; the service caller is recorded
separately. A request field such as `kind`, `actor`, `cashier`, or
`employee_id` is descriptive input and is never proof of either identity.

For the current same-process deployment, the sales-owned inventory adapter
marks the internal caller as sales. Inventory policy distinguishes direct stock adjustment
(`seller.inventory.manage`) from checkout and void stock effects
(`seller.sales.checkout` and `seller.sales.void`). Sales must authorize its
use case before invoking inventory, while inventory independently validates the
session and the delegated capability. This avoids granting cashiers general
inventory management rights merely to complete sales.

When a module is deployed remotely, an in-process capability is insufficient.
The caller must authenticate as a service and present a short-lived,
audience-restricted delegated credential binding the subject, actor service,
tenant, and permitted operation. OAuth 2.0 Token Exchange defines subject and
actor tokens and the `act` claim for this delegation shape (RFC 8693). The
resource service still performs its own policy decision; a gateway check or
network location is not a substitute. NIST SP 800-207A calls for application
and service identity policies alongside user identity policies, while NIST SP
800-162 supplies the subject/action/object/context policy model.

**Implementation status:** sales and inventory gRPC handlers now validate a
live session and enforce the existing permission keys. Sales derives cashier
and void actor fields from the validated session; inventory derives movement
actor from that session and ignores request actor fields. The sales inventory
adapter presents a random process-local capability over private `bufconn`.
This protects the current in-process caller boundary, but is not a network
service identity credential. Before network separation,
replace it with service authentication and a short-lived, audience-restricted
delegated credential. Inventory movement records persist employee subject and
service actor; authorization denials at sales/inventory boundaries are emitted
as structured logs without session identifiers. Central log retention, tamper
protection, review, and durable authorization-decision records remain open.
Legacy employee CRUD RPCs without session or permission inputs now return
`Unimplemented`. The roster read is retained with a session and the explicit
`authz.employee.read` permission, and derives tenant from that session. A future
employee lifecycle contract must also be session-bound and use explicitly
granted permissions.

**Implemented baseline (2026-10-16):** auth now has a permission-check RPC
bound to a live session, a Postgres evaluator over current tenant role/grant
metadata, and deny-by-default capability checks on seller routes. The supported
seller capability keys are seeded as metadata. Session-authorized PAP RPCs
create roles, grant and revoke supported permissions, and assign or revoke
time-bounded employee roles; read RPCs expose tenant roles and per-employee
assignment history to authorized policy administrators. Each write records actor employee, database
principal, change reference, and reason where applicable in the same
transaction. The last active grant/assignment for each PAP capability cannot
be removed, avoiding administrative lockout. Sales and inventory also enforce
permissions at their gRPC module boundaries; gateway checks remain defense in
depth.

1. Replace the singular role value in the auth domain and contracts with a
   repeated role/assignment representation while retaining a read-only
   compatibility field for one contract migration window.
2. Add role, permission-catalog, role-permission, and temporal employee-role
   assignment persistence with a migration that preserves current assignments.
3. ✅ Add role and permission administration APIs gated by the current session's
   bootstrap authority; role creation, grants, revocations, and assignments are
   audited transactionally.
4. ✅ Add PDP decisions and route PEP checks. Add the same decision at
   application mutation boundaries in owning services before production use.
5. Add relationship providers only when seller/store membership data has an
   agreed owner and source of truth. Consider an external engine only when
   measured policy complexity or cross-service policy governance justifies it.
6. Add durable decision/audit evidence and bounded caching only after the event
   scope is approved and the performance baseline supplies a cache budget.

## Open deployment decision

An initial policy administrator must be provisioned before the first role grant
can be managed. A one-time operator CLI is implemented as the proposed
bootstrap ceremony. It uses a separate `AUTHZ_DATABASE_URL`, requires an active
employee, tenant, and approved change reference, and records PostgreSQL's
`session_user`. The deployment/security owner still needs to ratify the operator
identity, unique-login requirement, and production approval path (OD-5). Until
then, do not run this command in production. The runtime remains default-deny;
no permission is granted to legacy role names.

The command is `go run ./cmd/authzbootstrap -tenant <tenant> -employee
<employee-id> -change-reference <change-id>`. It applies auth migrations and,
in one transaction, creates the tenant's reserved policy-admin
role, grants only the three PAP permissions, assigns it to the active employee,
and writes an admin event. A tenant-scoped advisory lock and existing active
policy grant check prevent a second bootstrap. The PostgreSQL login must be
individual to the deployment operator: `session_user` identifies the database
principal and does not by itself prove which human used a shared credential.
