# MowfNet — Project Bug Tracker

**Project:** MowfNet (IPAM & Network Inventory Management System)
**Document:** Canonical Bug, Vulnerability & Hardening Tracker
**Branch:** `feature/M2-dynamic-ip-pool`
**Candidate SHA:** `22cca7fc61458a32161f4155e20285a31192d515`
**Date:** 2026-08-26

---

## 1. Tracker Rules & Status Legend

- `[x]` = Fixed / Closed / Verified invariant
- `[ ]` = Open finding / Actionable task / Open hardening gap
- `[-]` = Explicitly deferred by architecture decision (non-blocking for current milestone)
- `[?]` = Needs investigation / Reproduction pending

### Priority Convention

```text
P0 = CRITICAL   (data corruption, orphan state, invalid committed network state, security vulnerability)
P1 = HIGH       (incorrect public API result, realistic race condition, count/state corruption)
P2 = MEDIUM     (bounded correctness issue, incorrect error semantics, resource robustness issue)
P3 = LOW        (minor robustness issue with limited impact)
P4 = HARDENING  (no demonstrated production failure; missing test proof or defensive improvement)
P5 = INFO       (architectural observation or performance note with no correctness defect)
```

---

## 2. Summary Dashboard

| Category | Total | `[x]` Fixed | `[ ]` Open | `[-]` Deferred |
|---|---|---|---|---|
| **M1 Historical Findings** | 8 | 8 | 0 | 0 |
| **M1 Hardening & Debt** | 3 | 0 | 3 | 0 |
| **M2 Historical & Protected** | 10 | 10 | 0 | 0 |
| **M2 Audit Findings** | 4 | 1 | 2 | 1 |
| **M2 Concurrency Hardening** | 3 | 0 | 3 | 0 |
| **M2 Info / Observations** | 1 | 0 | 1 | 0 |
| **Total** | **29** | **19** | **9** | **1** |

---

## 3. M1 — Historical Fixed Findings

### [x] M1-BUG-01 — Subnet list pagination contract violation

- **Category:** API / PAGINATION
- **Priority:** P1
- **Status:** FIXED
- **Evidence Commit:** `7452351d190ff9304c4e9b7c174c130432b00957` (`fix(ipam): enforce subnet list pagination contract`)
- **Summary:** Enforces strict limit contract: omitted defaults to 50; 1..100 accepted; <=0, >100, and non-numeric return `400 INVALID_REQUEST`. Out-of-range values are never silently clamped.

### [x] M1-BUG-02 — Subnet Resize lock-contract mismatch

- **Category:** CONCURRENCY / TRANSACTION
- **Priority:** P1
- **Status:** FIXED
- **Evidence Commit:** `880e7f5dbfe0b6c884e88fb3be97848019362c15` (`fix(ipam): align subnet resize lock contract`)
- **Summary:** When PATCH contains `cidr` (including same-CIDR requests), transaction enters `BEGIN -> pg_advisory_xact_lock(SubnetCoordinationKey) -> target Subnet FOR UPDATE`. This eliminates lock-order inversion between Create and Resize.

### [x] M1-BUG-03 — CIDR error classification was insufficiently specific

- **Category:** DOMAIN / ERROR CLASSIFICATION
- **Priority:** P2
- **Status:** FIXED
- **Summary:** Distinguished domain sentinel errors: `ErrNonCanonicalCIDR`, `ErrUnsupportedPrefixLength`, and `ErrIPv6NotSupported`. Verified with exact `errors.Is()` assertions.

### [x] M1-BUG-04 — Resize could invalidate persisted allocations

- **Category:** DATA INTEGRITY / RESIZE
- **Priority:** P0
- **Status:** FIXED
- **Summary:** Candidate CIDR must contain all existing child allocations strictly within usable host range (`first_usable` to `last_usable`). Allocations falling on new network or broadcast addresses trigger `409 SUBNET_RESIZE_CONFLICT`.

### [x] M1-BUG-05 — Subnet Delete needed allocation protection

- **Category:** DATA INTEGRITY / DELETE
- **Priority:** P0
- **Status:** FIXED
- **Summary:** Protected flow: `BEGIN -> target Subnet FOR UPDATE -> allocation EXISTS check -> if exists: 409 SUBNET_HAS_ALLOCATIONS -> DELETE -> COMMIT`. Database-level guard `ip_allocations_subnet_id_fkey ON DELETE RESTRICT` serves as final authority.

### [x] M1-BUG-06 — Subnet Delete FK error mapping needed exact classification

- **Category:** DATABASE / ERROR CLASSIFICATION
- **Priority:** P2
- **Status:** FIXED
- **Summary:** Only exact PostgreSQL condition `SQLSTATE 23503` AND `constraint = ip_allocations_subnet_id_fkey` maps to `ErrSubnetHasAllocations`. Unrelated FK violations remain internal errors.

### [x] M1-BUG-07 — Subnet allocation counts needed persisted-row authority

- **Category:** DATA CONSISTENCY / COUNTS
- **Priority:** P1
- **Status:** CLOSED / PROTECTED
- **Summary:** Derived allocation counts are computed directly from persisted rows (`assigned_count = COUNT(status='assigned')`, `reserved_count = COUNT(status='reserved')`, `available_count = usable_count - assigned_count - reserved_count`). No stale cache or uncoordinated counter is used.

### [x] M1-BUG-08 — Partial PATCH needed lost-update protection

- **Category:** CONCURRENCY / PATCH
- **Priority:** P1
- **Status:** CLOSED / PROTECTED
- **Summary:** PATCH reconstructs candidate state from current row locked with `FOR UPDATE` inside transaction rather than stale pre-transaction read.

---

## 4. M1 — Hardening & Technical Debt

### [ ] M1-HARDEN-01 — Advisory lock test observation could be scoped more tightly

- **Category:** TEST QUALITY / HARDENING
- **Priority:** P4
- **Status:** OPEN
- **Summary:** `waitForPostgresCondition` / `advisoryLockCount` checks `pg_locks` globally. Scope could be bound to exact PostgreSQL backend PID and intended transaction to avoid potential false positives under parallel execution.

### [ ] M1-HARDEN-02 — Dedicated Subnet Delete vs child allocation INSERT race test

- **Category:** CONCURRENCY / TEST COVERAGE
- **Priority:** P4
- **Status:** OPEN
- **Summary:** Current parent-row locking and FK `ON DELETE RESTRICT` semantics are safe, but a dedicated deterministic barrier test would strengthen regression coverage.

### [ ] M1-INFO-01 — Redundant `lib/pq` blank import in migration test

- **Category:** CLEANUP / INFO
- **Priority:** P5
- **Status:** OPEN
- **Summary:** Minor cosmetic redundant blank import in test file. No functional or correctness impact.

---

## 5. M2 — Historical Fixed / Protected Findings

### [x] M2-BUG-01 — Concurrent Reserve of same address must have one winner

- **Category:** CONCURRENCY / DATABASE
- **Priority:** P1
- **Status:** CLOSED / PROTECTED
- **Summary:** Database constraint `UNIQUE(address)` (`ip_allocations_address_uq`) serves as final authority. SQLSTATE `23505` with exact constraint name maps to `409 IP_ALREADY_ALLOCATED`. Tested deterministically at repository and HTTP layers.

### [x] M2-BUG-02 — Reserve must validate against current locked Subnet CIDR

- **Category:** CONCURRENCY / DATA INTEGRITY
- **Priority:** P0
- **Status:** CLOSED / PROTECTED
- **Summary:** Reserve locks parent subnet with `FOR KEY SHARE`, reconstructs current locked CIDR, and validates host usability before INSERT. Prevents stale-CIDR reservation during concurrent Subnet Resize.

### [x] M2-BUG-03 — Reserve vs Subnet Delete must not create orphan allocation

- **Category:** CONCURRENCY / DATA INTEGRITY
- **Priority:** P0
- **Status:** CLOSED / PROTECTED
- **Summary:** Reserve `FOR KEY SHARE` conflicts with Subnet Delete `FOR UPDATE`. Combined with `ON DELETE RESTRICT` FK, orphan allocations are prevented. Deterministic tests cover both execution orderings.

### [x] M2-BUG-04 — Concurrent Unreserve must not produce two successful deletes

- **Category:** CONCURRENCY / DELETE
- **Priority:** P1
- **Status:** CLOSED / PROTECTED
- **Summary:** Unreserve locks allocation row via `SELECT ... FOR UPDATE`. Competing transactions serialize; winner gets `204 No Content`, loser observes deleted row as missing and returns `404 IP_ALLOCATION_NOT_FOUND`.

### [x] M2-BUG-05 — Assigned allocation must not be deleted by Unreserve

- **Category:** STATE MACHINE / DATA INTEGRITY
- **Priority:** P1
- **Status:** CLOSED / PROTECTED
- **Summary:** Unreserve validates locked allocation status. Status `assigned` returns `409 IP_NOT_ASSIGNABLE`. Row and interface association remain untouched.

### [x] M2-BUG-06 — Unreserve must not report 204 when DELETE affects zero rows

- **Category:** TRANSACTION / DATA INTEGRITY
- **Priority:** P1
- **Status:** FIXED / PROTECTED
- **Summary:** Repository verifies `RowsAffected() == 1`. Any other result triggers transaction rollback and internal error; zero-row delete never returns 204.

### [x] M2-BUG-07 — Unreserve 204 response must have an empty body

- **Category:** HTTP / CONTRACT
- **Priority:** P2
- **Status:** FIXED / PROTECTED
- **Summary:** Unreserve handler returns `204 No Content` with zero body length (`w.WriteHeader(http.StatusNoContent)`).

### [x] M2-BUG-08 — Available addresses must never be persisted

- **Category:** STATE MODEL / DATABASE
- **Priority:** P1
- **Status:** CLOSED / INVARIANT
- **Summary:** `available` state is strictly derived in-memory (`usable - assigned - reserved`). Database only persists `reserved` and `assigned` statuses.

### [x] M2-BUG-09 — Available-IP enumeration must remain bounded on huge Subnets

- **Category:** PERFORMANCE / CORRECTNESS
- **Priority:** P2
- **Status:** CLOSED / PROTECTED
- **Summary:** Range scanning uses `availableIPScanBudget = 4096` to prevent unbounded memory allocation or full `/1` materialization.

### [x] M2-BUG-10 — Available-IP cursor must preserve forward progress

- **Category:** PAGINATION / CURSOR
- **Priority:** P1
- **Status:** CLOSED / PROTECTED
- **Summary:** Opaque cursor encapsulates subnet ID, effective range, and `last_examined` address. Resumption starts strictly at `last_examined + 1` with tampering and boundary checks.

---

## 6. M2 — Adversarial Audit Findings

### [-] M2-AUDIT-01 — Assigned Interface uniqueness missing from staged schema

- **Category:** DATABASE / CONTRACT / DEFERRED
- **Priority:** P1 (HIGH)
- **Status:** DEFERRED (`[-]`)
- **Target Milestone:** M4 (Assignment Milestone)
- **Owner:** 02 — Technical Lead / System Architect
- **Disposition & Rationale:**
  - ERD specifies `UNIQUE(interface_id) WHERE status = 'assigned'` and `interface_id REFERENCES device_interfaces(id)`.
  - M2 implements only reservation (`status='reserved'`, `interface_id=NULL`).
  - M2 does not implement Assign/Unassign, Devices, Interfaces, or `device_interfaces` table.
  - **Decision:** Not an M2 merge blocker. New migration `000004` and constraints will be added in M4 when Interface assignment workflows are created. Existing migration `000003` must not be modified.

### [x] M2-AUDIT-02 — HEAD `/api/v1/ip-allocations` rejected with 405

- **Category:** HTTP / API CONTRACT
- **Priority:** P2 (MEDIUM)
- **Status:** FIXED / CLOSED
- **Affected File:** `internal/ipam/http/allocation_handler.go`
- **Problem:** Route uses Go 1.22+ pattern `GET /api/v1/ip-allocations`, which routes both GET and HEAD. `ListAllocations` contains a redundant manual `r.Method != http.MethodGet` guard, causing HEAD requests to be rejected with `405 Method Not Allowed`.
- **Expected Fix:** Remove redundant manual method guard and add HEAD regression test.

### [ ] M2-AUDIT-03 — Reservation POST body has no explicit size bound

- **Category:** HTTP / SECURITY / RESOURCE ROBUSTNESS
- **Priority:** P2 (MEDIUM)
- **Status:** OPEN
- **Affected Endpoint:** `POST /api/v1/ip-allocations`
- **Problem:** Request body reaches `json.NewDecoder` without explicit size bounding (e.g. `http.MaxBytesReader`), creating potential memory exhaustion risk on oversized payloads.
- **Expected Fix:** Wrap `r.Body` with `http.MaxBytesReader` appropriate for reservation payloads and add oversized-payload regression test.

### [ ] M2-AUDIT-04 — Duplicate JSON object keys accepted

- **Category:** HTTP / INPUT VALIDATION / HARDENING
- **Priority:** P2 (MEDIUM / HARDENING)
- **Status:** OPEN
- **Affected Endpoint:** `POST /api/v1/ip-allocations`
- **Problem:** Decoder parses into `map[string]json.RawMessage`, so duplicate JSON keys (e.g. `{"subnet_id": 1, "subnet_id": 2}`) are accepted with last-key-wins semantics rather than rejected.
- **Expected Fix:** Add duplicate key detection/rejection and regression test suite.

---

## 7. M2 — Concurrency Hardening Gaps

These scenarios are statically proven safe under PostgreSQL `READ COMMITTED` isolation, but dedicated deterministic runtime barrier tests are recommended.

### [ ] M2-HARDEN-01 — X1: Unreserve vs Resize deterministic coverage

- **Category:** CONCURRENCY / HARDENING
- **Priority:** P4
- **Status:** OPEN
- **Scenario:** Concurrent `Unreserve(allocation_id)` and Subnet `Resize` shrinking CIDR below target address.
- **Static Analysis:** Proven safe. Resize sees allocation before Unreserve commit -> returns `409 SUBNET_RESIZE_CONFLICT`; or Unreserve commits first -> allocation deleted -> Resize proceeds cleanly. No orphan allocation possible.
- **Required Action:** Add deterministic trigger-paused concurrency test verifying both execution orderings.

### [ ] M2-HARDEN-02 — X2: Unreserve vs Subnet Delete deterministic coverage

- **Category:** CONCURRENCY / DATABASE / HARDENING
- **Priority:** P4
- **Status:** OPEN
- **Scenario:** Concurrent `Unreserve(allocation_id)` and `DeleteSubnet(subnet_id)`.
- **Static Analysis:** Proven safe. Delete holds `subnets FOR UPDATE` and checks `EXISTS`. `ip_allocations_subnet_id_fkey ON DELETE RESTRICT` protects integrity. No orphan possible.
- **Required Action:** Add deterministic trigger-paused concurrency test verifying clean deletion / conservative conflict.

### [ ] M2-HARDEN-03 — X3: Reserve vs Unreserve same address deterministic coverage

- **Category:** CONCURRENCY / DATABASE / HARDENING
- **Priority:** P4
- **Status:** OPEN
- **Scenario:** Concurrent `Unreserve(old_id)` and `Reserve(same_address)`.
- **Static Analysis:** Proven safe. Reserve INSERT blocks on `UNIQUE(address)` index lock. If Unreserve commits, INSERT unblocks and succeeds. If Unreserve rolls back, Reserve receives `409 IP_ALREADY_ALLOCATED`. Max one final row.
- **Required Action:** Add deterministic concurrency test verifying index contention, unblocking behavior, and final row count = 1.

---

## 8. M2 — Info & Performance Observations

### [ ] M2-INFO-01 — AvailableIPService Subnet read also calculates allocation counts

- **Category:** PERFORMANCE / INFO
- **Priority:** P5
- **Status:** OPEN
- **Summary:** `AvailableIPService.ListAvailableIPs` calls `s.subnets.GetByID()` which includes `LEFT JOIN ip_allocations` to count assigned/reserved allocations. Available-IP logic only requires the subnet CIDR. Consider adding a lightweight CIDR-only lookup method if profiling shows bottleneck on heavily populated subnets.

---

## 9. Actionable Pre-Merge Queue

### High / Medium Priority (Fix Before Merge)

1. `[ ]` **M2-AUDIT-02** — Remove manual method check in `ListAllocations` to support HEAD requests.
2. `[ ]` **M2-AUDIT-03** — Add `http.MaxBytesReader` to reservation POST endpoint.
3. `[ ]` **M2-AUDIT-04** — Add duplicate JSON key rejection handling.

### Concurrency Hardening (Test Before Merge)

4. `[ ]` **M2-HARDEN-01** — X1 Unreserve vs Resize deterministic test.
5. `[ ]` **M2-HARDEN-02** — X2 Unreserve vs Subnet Delete deterministic test.
6. `[ ]` **M2-HARDEN-03** — X3 Reserve vs Unreserve same-address deterministic test.

### Deferred (Post-M2 Roadmap)

- `[-]` **M2-AUDIT-01** — Add assigned interface uniqueness and FK constraint in M4 migration.
