# REQUIREMENTS — Phase 1: Know the Network

**Project:** IPAM & Network Inventory Management System  
**Milestone:** M0 — Requirements & Design  
**Owner:** 02 — Technical Lead / System Architect  
**Status:** M0 DESIGN BASELINE  
**Date:** 2026-08-24

## 1. Purpose

Phase 1 builds a centralized source of truth for IPv4 addressing and basic network inventory, with basic ICMP reachability monitoring. The system is intentionally designed as the first stage of a longer roadmap:

```text
KNOW THE NETWORK
    ↓
OBSERVE THE NETWORK
    ↓
CONTROL THE NETWORK
```

Phase 1 must be independently complete, testable and demoable without pulling Phase-2/Phase-3 features into the core.

## 2. Primary actor

### Administrator — LOCKED

Phase 1 supports exactly **one Administrator**.

The Administrator can:

- authenticate;
- manage Subnets;
- view derived IPv4 availability;
- reserve and unreserve IPv4 addresses;
- manage Devices and Interfaces;
- assign and unassign IPv4 addresses to Interfaces;
- manage the VLAN/Subnet relationship at the level approved for Phase 1;
- configure Basic ICMP Monitoring;
- view Dashboard summaries;
- search/filter the inventory.

There is no public registration, multi-user workflow or RBAC in Phase 1.

### Internal system actor

The monitoring scheduler/worker subsystem is an internal actor, not a human role. It performs background ICMP checks and persists monitoring state without blocking ordinary REST reads.

## 3. Core use cases

### UC-01 — Authenticate Administrator

1. Administrator submits credentials.
2. System verifies the stored password hash.
3. System establishes a server-side session.
4. Browser receives an HttpOnly session cookie.
5. Authenticated state-changing requests are CSRF-protected.
6. Administrator can log out and invalidate the session.

### UC-02 — Create and inspect a Subnet

1. Administrator submits an IPv4 CIDR.
2. System validates IPv4 scope and prefix `/1` through `/30`.
3. System rejects non-canonical CIDR input.
4. System rejects overlap with every existing Phase-1 Subnet.
5. System derives Network, Broadcast, First Usable, Last Usable and Usable Count.
6. System persists the Subnet.

### UC-03 — Resize a Subnet safely

Before changing a Subnet CIDR, the system must:

1. validate the new CIDR;
2. reject global overlap;
3. inspect all `reserved` and `assigned` allocations in the Subnet;
4. reject the resize if any persisted allocation falls outside the new usable range.

### UC-04 — Reserve / Unreserve an IPv4 address

Reserve:

1. address belongs to the target Subnet;
2. address is inside the usable range;
3. address is not Network/Broadcast;
4. address does not conflict with an existing persisted allocation;
5. system creates a `reserved` allocation with `interface_id = NULL`.

Unreserve removes the persisted `reserved` allocation, making the address derived `available` again.

### UC-05 — Manage Network Inventory

1. Administrator creates a Device.
2. Administrator creates one or more Interfaces for that Device.
3. Device-to-Interface ownership is preserved.
4. Phase 1 does not store `Device.ip_address`.

### UC-06 — Assign / Unassign IPv4

Assign:

- an `available` address may become `assigned` directly;
- a `reserved` allocation may become `assigned` directly;
- the assignment must reference an Interface;
- one Interface has at most one assigned IPv4 in Phase 1;
- concurrent requests for the same IP must result in one success and one conflict.

Unassign:

- `assigned → available`;
- it does not automatically return to `reserved`;
- if the allocation is a Monitoring Target, monitoring must be cleared/reset before releasing the allocation.

### UC-07 — Configure Basic ICMP Monitoring

1. Administrator creates or updates the main Monitoring Check for a Device.
2. Target, when present, must be one specific `assigned` IP Allocation.
3. The allocation must belong to an Interface owned by the same Device.
4. Reserved allocations are not valid targets.
5. Monitoring runs in the background.
6. State is `UNKNOWN`, `ONLINE` or `OFFLINE`.
7. Retry threshold prevents a single failed ping from immediately producing `OFFLINE`.
8. Stale results from old targets are discarded.

### UC-08 — View Dashboard and search/filter

Dashboard provides at minimum useful summaries for:

- Subnets;
- Usable / Assigned / Reserved / Available IP counts;
- Devices;
- Monitoring state counts.

Search/filter may cover:

- Device name;
- IP;
- monitoring status;
- Subnet;
- VLAN.

## 4. Mandatory Phase-1 core — LOCKED

### IPAM

- Subnet CRUD.
- IPv4 CIDR validation.
- Network/Broadcast calculation.
- Usable range/count.
- Global overlap prevention.
- Safe resize/delete.
- Dynamic IP Pool.
- Reserve / Unreserve.
- Assign / Unassign.
- Duplicate-IP prevention.
- Database constraints.
- Transaction-safe allocation.

### Network Inventory

- Device CRUD.
- Interface CRUD.
- `1 Device → N Interfaces`.
- At most one assigned IPv4 per Interface in Phase 1.
- IP Allocation linked to Interface.
- VLAN/Subnet relationship represented in the domain model.

### Basic Monitoring

- ICMP only.
- Dedicated Monitoring Check resource.
- Monitoring target is a specific assigned IP Allocation.
- `UNKNOWN / ONLINE / OFFLINE`.
- retry threshold;
- `last_check`;
- `last_seen`;
- background scheduler;
- bounded worker pool;
- job deduplication;
- stale-result protection.

### System

- Go backend.
- React frontend.
- PostgreSQL.
- REST API.
- Modular Monolith.
- one Administrator;
- Dashboard;
- search/filter;
- testing.

## 5. Optional if core is stable

The following must not delay the mandatory IPAM/Inventory core:

- full VLAN CRUD/UI polish;
- advanced filtering;
- Basic ICMP Discovery;
- unknown active IP detection;
- non-essential UI improvements.

The VLAN/Subnet **domain relationship** remains represented even if full VLAN UI is deferred.

## 6. Explicitly out of scope — LOCKED

- SNMP monitoring.
- Traffic/time-series history.
- Advanced Discovery.
- LLDP/CDP.
- Automatic topology.
- DNS management.
- DHCP management.
- Full alerting system.
- Multi-user/RBAC.
- IPv6.
- VRF.
- Multiple IP addresses per Interface.
- Ansible.
- SSH automation.
- Configuration deployment.
- Configuration backup/versioning.
- Rollback.
- Microservices.

The separate Python `network-monitor` project remains R&D and is not a mandatory Phase-1 dependency.

## 7. Demo target

A complete Phase-1 demo should be able to show:

1. Login Admin.
2. Create `192.168.10.0/24`.
3. Show Network/Broadcast/Usable range.
4. Reserve an IP.
5. Create Device.
6. Create Interface.
7. Assign an IP.
8. Duplicate assignment is rejected.
9. Show available count.
10. Create/configure ICMP Monitoring Check.
11. Select an assigned IP as target.
12. Successful ICMP → `ONLINE`.
13. Repeated failures to threshold → `OFFLINE`.
14. Success again → `ONLINE` and `last_seen` updates.
15. Unassign target IP.
16. IP becomes derived `available`.
17. Monitoring target is cleared/reset safely.
18. Search/filter works.

Target demo scale:

- approximately 5–10 Subnets;
- approximately 20–50 Devices/endpoints.

Initial design target is approximately 100–200 Devices. This is a design target, not a benchmark claim.

## 8. Non-functional scope

### Correctness

Correct network/domain modeling takes priority over feature count.

### Data integrity

Critical invariants are enforced through:

```text
Application Validation
    ↓
Database Transaction
    ↓
UNIQUE / CHECK / FK
    ↓
Commit
```

### Concurrency safety

At minimum:

- concurrent same-IP assignment must not produce two successful assignments;
- concurrent overlapping Subnet creation/update must not bypass overlap protection;
- monitoring jobs for one check are deduplicated;
- stale monitoring results must not overwrite current target state.

### Maintainability

- Modular Monolith.
- Backend boundaries: Auth / Inventory / IPAM / Monitoring.
- Business logic is not concentrated in REST handlers.
- Phase 1 should remain clean enough to evolve into NMS without rewriting the core.

### Security

- one Administrator;
- passwords stored only as hashes;
- session authentication;
- HttpOnly cookie;
- CSRF protection on authenticated state-changing requests;
- secrets are not committed to Git.

### Monitoring behavior

- normal REST read requests must not synchronously ping all Devices;
- monitoring concurrency is bounded;
- no distributed scheduler/lock is required in Phase 1.

### Deployment/test environment

Validated in authorized lab/private-network environments such as VM, homelab, GNS3 or EVE-NG. Do not claim production enterprise readiness without evidence.

### Performance claims

No hard latency/SLA claim is part of M0. Claims such as “response time < 1s” require measurement before publication.

## 9. M0 technical decisions affecting Requirements

| ID | Decision | Status |
|---|---|---|
| M0-TD-01 | Subnet input must already be canonical; `192.168.1.10/24` is rejected instead of silently normalized. | M0-LOCKED |
| M0-TD-02 | Concurrent Subnet create/resize operations must be serialized around overlap validation + write using PostgreSQL `pg_advisory_xact_lock` with one project-defined Subnet coordination key. | M0-LOCKED |
| M0-TD-03 | REST contract is versioned under `/api/v1`. | M0-LOCKED |
| M0-TD-04 | Interface ownership (`device_id`) is immutable through normal Interface update in Phase 1; moving an Interface requires recreate/dedicated future workflow. | M0-LOCKED |
| M0-TD-05 | Monitoring target removal keeps the Monitoring Check, clears the target and resets monitoring state to `UNKNOWN`. | M0-LOCKED |
| M0-TD-06 | Available-IP queries are generated on demand; the API never enumerates an entire huge Subnet by default. | M0-LOCKED |

These decisions resolve previously OPEN implementation choices and do not modify any prior LOCKED architecture decision.

## 10. Remaining OPEN decisions

These do not block M1 unless explicitly stated otherwise.

1. **VLAN ID validation details** — exact allowed range/format and DB checks must be finalized before INV-06.  
2. **Full VLAN functionality priority** — relationship is in the model, but PM must decide whether full CRUD/UI is mandatory or optional for the final Phase-1 delivery.  
3. **Disabled Monitoring dashboard semantics** — must be finalized before M6/M7; no new `DISABLED` monitoring state may be introduced without an Architecture Change Request because the locked state set is `UNKNOWN/ONLINE/OFFLINE`.  
4. **Exact ICMP implementation on Go/Linux** — raw socket/unprivileged ping/capability/container mechanism to be selected before MON-03.  
5. **React + session-cookie development topology** — same-origin vs split-origin development must be selected before Auth/Frontend integration; CORS/SameSite/credentials behavior follows that choice.  
6. **Device/Interface/MAC naming uniqueness** — no uniqueness rule is added in M0 because baseline does not define one; IDs remain authoritative until a business rule is explicitly approved.  
7. **`network-monitor` production role** — intentionally Future/Phase 2; no Phase-1 decision required.

## 11. M0-01 acceptance

Requirements are ready when Engineering agrees that:

- Phase-1 actor and scope are explicit;
- core use cases are understood;
- mandatory vs optional vs future work is separated;
- demo target and non-functional scope are explicit;
- no implementation assumes `Device.ip_address`;
- remaining OPEN items are visible and assigned to the milestone where they matter.
