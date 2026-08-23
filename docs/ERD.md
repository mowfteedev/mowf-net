# ERD v1 & DATABASE CONSTRAINTS PLAN

**Project:** IPAM & Network Inventory Management System  
**Milestone:** M0-05 / M0-06  
**Owner:** 02 — Technical Lead / System Architect  
**Status:** M0 DESIGN BASELINE  
**Date:** 2026-08-24

## 1. ERD v1

```mermaid
erDiagram
    USERS ||--o{ SESSIONS : has
    DEVICES ||--o{ DEVICE_INTERFACES : owns
    VLANS ||--o{ SUBNETS : groups
    SUBNETS ||--o{ IP_ALLOCATIONS : contains
    DEVICE_INTERFACES o|--o| IP_ALLOCATIONS : assigned_to
    DEVICES ||--o| MONITORING_CHECKS : has_main_check
    IP_ALLOCATIONS o|--o{ MONITORING_CHECKS : targeted_by

    USERS {
        bigint id PK
        string username UK
        string password_hash
        timestamp created_at
    }

    SESSIONS {
        bigint id PK
        bigint user_id FK
        string token_hash UK
        string csrf_token_hash
        timestamp expires_at
        timestamp created_at
        timestamp revoked_at
    }

    DEVICES {
        bigint id PK
        string name
        string type
        string location
        string description
        timestamp created_at
        timestamp updated_at
    }

    DEVICE_INTERFACES {
        bigint id PK
        bigint device_id FK
        string name
        string mac_address
        timestamp created_at
        timestamp updated_at
    }

    VLANS {
        bigint id PK
        int vlan_id
        string name
        string description
    }

    SUBNETS {
        bigint id PK
        bigint vlan_id FK_NULL
        string network
        int prefix_length
        string description
        timestamp created_at
        timestamp updated_at
    }

    IP_ALLOCATIONS {
        bigint id PK
        bigint subnet_id FK
        string address UK
        string status
        bigint interface_id FK_NULL
        string description
        timestamp created_at
        timestamp updated_at
    }

    MONITORING_CHECKS {
        bigint id PK
        bigint device_id FK_UK
        bigint target_ip_allocation_id FK_NULL
        string type
        boolean enabled
        string status
        timestamp last_check
        timestamp last_seen
        int consecutive_failures
        timestamp created_at
        timestamp updated_at
    }
```

## 2. Cardinality and ownership

### Device → Interface — LOCKED

```text
1 Device → N Interfaces
```

`device_interfaces.device_id` is required.

### Interface → Assigned IP Allocation — LOCKED Phase-1 limit

```text
1 Interface → 0..1 assigned IPv4
```

A reserved allocation has no Interface.

### Subnet → IP Allocation

```text
1 Subnet → N persisted allocations
```

Only `reserved` and `assigned` allocations exist as rows.

### VLAN → Subnet

```text
1 VLAN → N Subnets
1 Subnet → 0..1 VLAN
```

### Device → Monitoring Check

```text
1 Device → 0..1 main ICMP Monitoring Check
```

### Monitoring Check → Target Allocation

```text
1 Monitoring Check → 0..1 target allocation
```

When a target exists it must be assigned and owned by an Interface of the same Device.

## 3. Persistence decisions — M0

### IDs — M0-LOCKED

Use PostgreSQL integer identity keys represented as `BIGINT` at the database/API boundary.

The API treats IDs as opaque identifiers and clients must not infer ordering semantics from them.

### Subnet representation — M0-LOCKED

Persist:

- `network INET NOT NULL` — canonical IPv4 network address stored as a host-form `/32` value;
- `prefix_length SMALLINT NOT NULL` — integer `/1..30`.

Required checks:

```text
family(network) = 4
masklen(network) = 32
prefix_length BETWEEN 1 AND 30
```

The canonical rule ensures the host-form `network` value is exactly the network address for the supplied prefix before persistence. The API reconstructs CIDR as `network/prefix_length`.

Network/Broadcast/usable range/count are derived values, not separately authoritative columns.

### IP address representation — M0-LOCKED

Persist only host addresses for occupied allocations as `address INET NOT NULL`, stored in host-form `/32`. Required checks:

```text
family(address) = 4
masklen(address) = 32
```

The application/API representation remains a normal IPv4 string. No row is created for `available`.

### Sessions — M0-LOCKED

A minimal server-side `sessions` resource is added to the ERD to implement the already-approved Session Authentication direction reliably across requests/restarts.

Session tokens and CSRF tokens are stored as non-plaintext token hashes where applicable.

## 4. Required database constraints

### `users`

- PK: `id`.
- `username NOT NULL`.
- `UNIQUE(username)`.
- `password_hash NOT NULL`.

### `sessions`

- PK: `id`.
- FK `user_id → users.id`.
- `UNIQUE(token_hash)`.
- `expires_at NOT NULL`.
- index on `user_id`.
- index on `expires_at` for cleanup.

### `devices`

- PK: `id`.
- `name NOT NULL`.
- no M0 uniqueness constraint on `name`.

### `device_interfaces`

- PK: `id`.
- FK `device_id → devices.id` with delete behavior that does not bypass explicit domain delete workflow.
- `name NOT NULL`.
- index on `device_id`.
- no M0 uniqueness constraint on `name` or `mac_address` because baseline does not define one.

### `vlans`

- PK: `id`.
- exact CHECK/UNIQUE policy for `vlan_id` remains OPEN before INV-06.

### `subnets`

- PK: `id`.
- nullable FK `vlan_id → vlans.id` with `ON DELETE RESTRICT` semantics.
- `network INET NOT NULL`.
- `prefix_length SMALLINT NOT NULL`.
- `CHECK(family(network) = 4)`.
- `CHECK(masklen(network) = 32)`.
- `CHECK(prefix_length BETWEEN 1 AND 30)`.
- `UNIQUE(network, prefix_length)` prevents exact duplicates.
- global overlap is a domain + transactional invariant, not solved by this UNIQUE constraint alone.

### `ip_allocations`

- PK: `id`.
- FK `subnet_id → subnets.id`, delete restricted while allocations remain.
- `address INET NOT NULL`.
- `CHECK(family(address) = 4)`.
- `CHECK(masklen(address) = 32)`.
- `UNIQUE(address)` — valid because Phase 1 forbids overlapping Subnets/VRFs.
- `status NOT NULL`.
- `CHECK(status IN ('reserved','assigned'))`.
- nullable FK `interface_id → device_interfaces.id` with delete restricted until explicit unassign workflow.
- status/interface consistency CHECK:

```text
(status = 'reserved' AND interface_id IS NULL)
OR
(status = 'assigned' AND interface_id IS NOT NULL)
```

- one assigned IPv4 per Interface: partial unique constraint/index equivalent to:

```text
UNIQUE(interface_id) WHERE status = 'assigned'
```

- index on `subnet_id`.
- index on `interface_id`.

### `monitoring_checks`

- PK: `id`.
- required FK `device_id → devices.id`.
- `UNIQUE(device_id)` for at most one main check per Device.
- nullable FK `target_ip_allocation_id → ip_allocations.id` with delete restricted until target clear/reset workflow.
- `CHECK(type = 'ICMP')` in Phase 1.
- `CHECK(status IN ('UNKNOWN','ONLINE','OFFLINE'))`.
- `CHECK(consecutive_failures >= 0)`.
- default `status='UNKNOWN'`.
- default `consecutive_failures=0`.
- index on `target_ip_allocation_id`.
- index on `(enabled, status)` only if query evidence requires it; not correctness-critical.

## 5. Cross-table invariants

Some invariants cannot be represented by a simple FK/CHECK without duplicating domain data.

### Monitoring Target ownership

Required:

```text
MonitoringCheck.target allocation
→ status = assigned
→ allocation.interface_id != NULL
→ allocation.interface.device_id == MonitoringCheck.device_id
```

Enforcement plan:

1. service validates inside a DB transaction;
2. rows needed for the decision are locked consistently;
3. target FK prevents dangling allocation references;
4. unassign/delete paths are blocked by FK until target is cleared;
5. stale-result persistence uses target identity in the update predicate.

A database trigger is not introduced in M0 because the transaction + FK + explicit workflow is sufficient for Phase-1 complexity and remains testable.

### IP inside Subnet usable range

The application/domain layer validates containment and usable range before insert/update. Database uniqueness/status constraints remain the final race-condition protection.

## 6. Transaction boundaries

### TX-SUBNET-CREATE — M0-LOCKED

```text
BEGIN
→ acquire `pg_advisory_xact_lock(SUBNET_COORDINATION_KEY)`
→ validate canonical CIDR
→ query global overlap
→ INSERT subnet
COMMIT
```

Concurrent Subnet create/resize requests use the same coordination key so that overlap checks cannot race.

### TX-SUBNET-RESIZE — M0-LOCKED

```text
BEGIN
→ acquire `pg_advisory_xact_lock(SUBNET_COORDINATION_KEY)`
→ lock target Subnet
→ validate new canonical CIDR
→ query overlap excluding current Subnet
→ inspect all persisted allocations in target Subnet
→ reject if any allocation leaves new usable range
→ UPDATE
COMMIT
```

### TX-RESERVE

```text
BEGIN
→ validate Subnet/address
→ INSERT reserved allocation
→ UNIQUE(address) resolves concurrent duplicate attempt
COMMIT
```

Constraint violation maps to `409 IP_ALREADY_ALLOCATED`.

### TX-UNRESERVE

```text
BEGIN
→ lock allocation row
→ require status=reserved
→ DELETE allocation
COMMIT
```

### TX-ASSIGN-AVAILABLE

```text
BEGIN
→ lock target Interface
→ verify Interface has no assigned IPv4
→ validate address/Subnet
→ INSERT assigned allocation
→ UNIQUE(address) + unique assigned interface protect races
COMMIT
```

### TX-ASSIGN-RESERVED

```text
BEGIN
→ lock target allocation
→ lock target Interface
→ require allocation.status=reserved
→ validate allocation/Subnet/Interface
→ UPDATE status=assigned, interface_id=...
→ constraints protect races
COMMIT
```

### TX-UNASSIGN

```text
BEGIN
→ lock assigned allocation
→ lock any Monitoring Check targeting it
→ clear target + reset monitoring if needed
→ DELETE allocation
COMMIT
```

### TX-DELETE-INTERFACE

```text
BEGIN
→ lock Interface
→ locate assigned allocation
→ clear/reset Monitoring Target if needed
→ release allocation
→ DELETE Interface
COMMIT
```

### TX-DELETE-DEVICE

```text
BEGIN
→ lock Device
→ handle Monitoring Check
→ release assigned allocations from all Interfaces
→ delete Interfaces
→ delete Device
COMMIT
```

### TX-MONITORING-TARGET-CHANGE

```text
BEGIN
→ lock Monitoring Check
→ lock target allocation + owning Interface
→ validate assigned + same Device
→ set target
→ reset status/counters/timestamps
COMMIT
→ enqueue immediate check
```

Queueing occurs after successful commit so workers never run against an uncommitted target.

### TX-MONITORING-RESULT

Result persistence must be conditional on current target identity.

Conceptually:

```text
UPDATE monitoring_checks
SET ...
WHERE id = job.check_id
  AND target_ip_allocation_id = job.target_allocation_id
```

If no row matches, result is stale and discarded.

## 7. Conflict cases and expected API mapping

| Case | Expected result |
|---|---|
| Invalid/non-canonical CIDR | `400 INVALID_CIDR` |
| Subnet overlap | `409 SUBNET_OVERLAP` |
| Resize ejects existing allocation | `409 SUBNET_RESIZE_CONFLICT` |
| Delete Subnet with allocations | `409 SUBNET_HAS_ALLOCATIONS` |
| Duplicate IP reserve/assign | `409 IP_ALREADY_ALLOCATED` |
| Interface already assigned | `409 INTERFACE_ALREADY_ASSIGNED` |
| Wrong allocation state | `409 IP_NOT_ASSIGNABLE` or state-specific code |
| Delete VLAN still referenced | `409 VLAN_HAS_SUBNETS` |
| Invalid monitoring target | `409 MONITORING_TARGET_INVALID` |
| Missing resource | `404 ..._NOT_FOUND` |
| Concurrent same-IP assignment | exactly one success; loser receives `409` |
| Concurrent overlapping Subnet create | one commits; other rechecks after serialization and receives `409` |

## 8. Delete behavior

Do not rely on broad cascade for Device/Interface/Subnet/VLAN domain operations.

FKs should prevent invalid deletion order and force service-layer workflows to execute the required cleanup sequence.

## 9. Remaining database OPEN items

These do not block M1 except where noted:

- exact VLAN ID CHECK/UNIQUE rules before INV-06;
- non-critical performance indexes should be evidence-driven;
- migration tool/package naming is implementation choice, not architecture;
- device/interface/MAC uniqueness remains OPEN business policy.

M1 **must not** reopen:

- canonical CIDR rejection;
- global overlap rule;
- serialized concurrent Subnet writes;
- `/1..30` scope.
