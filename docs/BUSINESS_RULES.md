# BUSINESS RULES — Phase 1

**Project:** IPAM & Network Inventory Management System  
**Milestone:** M0-02  
**Owner:** 02 — Technical Lead / System Architect  
**Status:** M0 DESIGN BASELINE  
**Date:** 2026-08-24

## 1. Rule labels

- **LOCKED** — inherited project baseline; Engineering must not change silently.
- **M0-LOCKED** — previously OPEN implementation/design detail resolved by Tech Lead during M0; changing it requires explicit technical review.
- **OPEN** — not yet locked; do not invent behavior during implementation.

## 2. Core domain invariants — LOCKED

```text
Device
  ↓ 1:N
Interface
  ↓ 0:1 assigned IPv4 in Phase 1
IP Allocation
  ↓ N:1
Subnet
  ↓ 0..1
VLAN
```

Monitoring:

```text
Device
  ↓ 0..1 main check
Monitoring Check
  ↓ 0..1 target
Assigned IP Allocation
```

Never model:

```text
Device.ip_address
```

## 3. Subnet rules

### SUBNET-01 — IPv4 scope — LOCKED

Phase 1 supports IPv4 prefixes `/1` through `/30` only.

Not supported:

- `/31`;
- `/32`;
- IPv6;
- VRF.

### SUBNET-02 — Canonical CIDR input — M0-LOCKED

A create/resize request must provide a canonical network CIDR.

Example:

```text
192.168.1.0/24   → valid
192.168.1.10/24  → reject
```

The server does not silently normalize host bits.

### SUBNET-03 — Derived values — LOCKED

For every valid Subnet, the system derives:

- Network Address;
- Broadcast Address;
- First Usable;
- Last Usable;
- Usable Count.

### SUBNET-04 — Global overlap prevention — LOCKED

Because Phase 1 has no VRF/routing domain, no two IPv4 Subnets may overlap anywhere in the system.

Overlap validation applies on:

- create;
- update/resize.

### SUBNET-05 — Concurrent overlap protection — M0-LOCKED

Application-level overlap checking alone is insufficient.

All Subnet create/resize writes must execute inside a database transaction that obtains PostgreSQL `pg_advisory_xact_lock` using one project-defined Phase-1 Subnet coordination key before:

1. overlap query;
2. allocation-safety checks for resize;
3. insert/update;
4. commit.

This serializes competing Subnet writes without introducing distributed locking.

### SUBNET-06 — Resize safety — LOCKED

Before a resize commits:

1. CIDR is valid and canonical;
2. no other Subnet overlaps;
3. all existing `reserved` and `assigned` allocations remain inside the new usable range.

Otherwise reject with conflict.

### SUBNET-07 — Delete safety — LOCKED

Delete Subnet is blocked while any `reserved` or `assigned` allocation remains in it.

The Administrator must unreserve/unassign first.

## 4. Dynamic IP Pool rules

### IP-01 — Available is derived — LOCKED

The database persists only:

```text
reserved
assigned
```

`available` is not persisted.

```text
Available = Usable Host Range - Reserved - Assigned
```

### IP-02 — No full address pre-generation — LOCKED

Never create a row for every available host in a Subnet.

### IP-03 — Common Reserve/Assign validation — LOCKED

An address must:

1. belong to the target Subnet;
2. be inside the usable host range;
3. not be Network Address;
4. not be Broadcast Address;
5. not already conflict with a persisted allocation;
6. satisfy database constraints.

### IP-04 — Reserved state — LOCKED

```text
status = reserved
interface_id = NULL
```

### IP-05 — Assigned state — LOCKED

```text
status = assigned
interface_id != NULL
```

### IP-06 — Per-Interface assignment limit — LOCKED

```text
1 Interface → at most 1 assigned IPv4
```

This is a Phase-1 simplification, not a future architectural limit.

### IP-07 — Direct Reserved → Assigned — LOCKED

A reserved allocation may be assigned directly to an Interface.

### IP-08 — Unassign semantics — LOCKED

```text
assigned → available
```

Unassign removes/releases the persisted allocation. It does not automatically return to `reserved`.

### IP-09 — Concurrent duplicate assignment — LOCKED

Two concurrent requests for the same IPv4 must produce:

```text
1 success
1 conflict
```

Never two successes.

Final protection requires transaction + database constraint.

## 5. Device rules

### DEV-01 — Device/Interface model — LOCKED

```text
1 Device → N Interfaces
```

### DEV-02 — Device fields

Baseline fields:

- name;
- type;
- location;
- description.

No additional uniqueness rule for `name` is assumed in M0.

### DEV-03 — Delete Device — LOCKED

Delete Device occurs in one transaction:

1. handle/delete its Monitoring Check;
2. clear/reset any target references as required;
3. release assigned IPs owned by its Interfaces;
4. delete Interfaces;
5. delete Device.

Broad database cascade is not used as a substitute for these domain steps.

## 6. Interface rules

### IF-01 — Ownership — LOCKED

Every Interface belongs to exactly one Device.

### IF-02 — Address ownership — LOCKED

An assigned IP belongs to an Interface, never directly to a Device.

### IF-03 — Transfer between Devices — M0-LOCKED

Normal Interface update cannot change `device_id`.

Phase 1 does not support direct:

```text
Device A / Interface X → Device B
```

Use delete/recreate or a future dedicated workflow.

### IF-04 — Delete Interface — LOCKED

If the Interface owns an assigned IP:

1. if the IP is a Monitoring Target, clear/reset Monitoring first;
2. unassign/release the IP;
3. delete Interface.

### IF-05 — Name/MAC uniqueness — OPEN

Baseline does not define uniqueness for:

- Interface name;
- MAC address.

M0 therefore does not invent a uniqueness rule. IDs are authoritative until an explicit rule is approved.

## 7. VLAN rules

### VLAN-01 — Relationship — BASELINE

```text
1 VLAN → N Subnets
1 Subnet → 0..1 VLAN
```

### VLAN-02 — Delete VLAN — LOCKED behavior

Delete VLAN is blocked while any Subnet references it.

### VLAN-03 — Delete Subnet asymmetry — LOCKED behavior

A Subnet referencing a VLAN may be deleted if the Subnet otherwise satisfies its own delete rules.

### VLAN-04 — Exact VLAN validation — OPEN

Exact `vlan_id` validation/DB constraints remain to be locked before INV-06.

### VLAN-05 — Delivery priority — PM DECISION REQUIRED

The domain relationship is part of Phase 1. Full VLAN CRUD/UI may remain optional if core schedule pressure exists. PM owns the final priority decision.

## 8. Monitoring rules

### MON-01 — Phase-1 protocol — LOCKED

ICMP only. No SNMP.

### MON-02 — One main check per Device — BASELINE

A Device has at most one main ICMP Monitoring Check in Phase 1.

### MON-03 — Target invariant — LOCKED

A Monitoring Target must:

1. reference an existing IP Allocation;
2. have `status = assigned`;
3. have non-null `interface_id`;
4. belong to an Interface owned by the same Device as the Monitoring Check.

Reserved IPs and another Device's allocation are invalid targets.

### MON-04 — States — LOCKED

```text
UNKNOWN
ONLINE
OFFLINE
```

No fourth monitoring state may be added silently.

### MON-05 — `last_check` — LOCKED semantics

Time of the latest monitoring attempt.

### MON-06 — `last_seen` — LOCKED semantics

Time of the latest successful ICMP response for the current target.

It does not mean “last time any traffic from the Device was observed”.

### MON-07 — Retry threshold — BASELINE

Do not mark OFFLINE after one failed check.

Default configuration:

```text
OFFLINE_THRESHOLD=3
```

On success:

```text
status = ONLINE
consecutive_failures = 0
last_check = now
last_seen = now
```

On failure:

```text
consecutive_failures += 1
last_check = now
```

If failures reach the threshold:

```text
status = OFFLINE
```

### MON-08 — Background architecture — LOCKED direction

Normal REST reads do not ping Devices.

```text
Scheduler
  ↓
Job Queue
  ↓
Bounded Worker Pool
  ↓
ICMP
  ↓
Result
  ↓
Database
```

### MON-09 — Cycle overlap — LOCKED

Do not start a new monitoring cycle while the previous cycle is still running.

### MON-10 — Job deduplication — LOCKED

One Monitoring Check has at most one active job at a time.

### MON-11 — Immediate Check — BASELINE

Immediate check is triggered when:

- a Monitoring Target is created;
- a Monitoring Target changes.

It is not triggered after every IP assignment.

### MON-12 — Stale-result protection — LOCKED

A job carries enough target identity to verify that its result still belongs to the current target.

If target identity changed before persistence, discard the result.

### MON-13 — Target removal — M0-LOCKED

When a targeted allocation is unassigned/deleted:

1. keep the Monitoring Check;
2. clear `target_ip_allocation_id`;
3. set `status = UNKNOWN`;
4. set `consecutive_failures = 0`;
5. set `last_check = NULL`;
6. set `last_seen = NULL`;
7. release the allocation.

This prevents dangling references.

### MON-14 — Target change reset — M0-LOCKED

When changing target A → target B:

- set new target;
- reset state to `UNKNOWN`;
- reset `consecutive_failures = 0`;
- reset `last_check` and `last_seen` because previous timestamps belong to the old target;
- enqueue an immediate check through the same queue used by scheduled checks.

### MON-15 — Disabled Monitoring semantics — OPEN

`enabled=false` is part of the conceptual model, but dashboard/state treatment remains OPEN before M6/M7.

A `DISABLED` monitoring state is not permitted without reopening the locked three-state model.

## 9. Authentication rules

### AUTH-01 — Single Administrator — LOCKED

Exactly one Administrator is supported in Phase 1.

No registration, multi-user or RBAC.

### AUTH-02 — Password storage — LOCKED

Store only `password_hash`. Never plaintext password.

### AUTH-03 — Bootstrap — BASELINE

Admin bootstrap must be idempotent:

```text
if admin exists → skip
else → hash password → create admin
```

`username` must be unique.

### AUTH-04 — Session auth — BASELINE

Use server-side session authentication with an HttpOnly cookie rather than access/refresh JWT complexity.

### AUTH-05 — CSRF — LOCKED direction

Authenticated state-changing methods require CSRF protection:

- POST;
- PUT;
- PATCH;
- DELETE.

A request header such as `X-CSRF-Token` is used by API Contract v1. Login obtains a short-lived pre-auth CSRF token first so the POST login request also follows the state-changing-request rule.

### AUTH-06 — Cookie considerations

Session cookies must consider:

- HttpOnly;
- SameSite;
- Secure when HTTPS is enabled.

Exact split-origin development behavior remains OPEN until the dev topology is selected.

## 10. Cross-module delete/order rules

### Delete Device

```text
Monitoring
  ↓
Assigned IPs
  ↓
Interfaces
  ↓
Device
```

### Delete Interface

```text
Monitoring target if any
  ↓
Unassign IP
  ↓
Interface
```

### Unassign targeted IP

```text
Clear target + reset UNKNOWN
  ↓
Release allocation
```

These sequences must execute atomically where multiple persistent resources are changed.

## 11. Business-rule conflict policy

If implementation requires behavior not covered here:

```text
STOP
→ mark OPEN
→ request Tech Lead decision
```

Do not infer a new business rule from frontend convenience or implementation shortcuts.
