# REST API CONTRACT v1

**Project:** IPAM & Network Inventory Management System  
**Milestone:** M0-07  
**Owner:** 02 — Technical Lead / System Architect  
**Status:** M0 DESIGN BASELINE  
**Date:** 2026-08-24

## 1. Contract scope

Base path:

```text
/api/v1
```

Resources:

```text
/auth
/devices
/interfaces
/vlans
/subnets
/ip-allocations
/monitoring
/dashboard
```

The API must distinguish:

- **persisted allocations**: `reserved`, `assigned`;
- **computed available addresses**: no allocation row and no allocation ID.

## 2. Authentication and CSRF

### Session model

- server-side session;
- HttpOnly session cookie;
- password hash only;
- one Administrator.

### CSRF

Authenticated state-changing requests use:

```http
X-CSRF-Token: <token>
```

Methods covered:

- POST;
- PUT;
- PATCH;
- DELETE.

`GET /auth/csrf` is available before login and after login. Before login it issues a short-lived pre-auth CSRF token/cookie pair; `POST /auth/login` must echo the token in `X-CSRF-Token`. After authentication, the session receives its own CSRF token for subsequent writes.

Cookie SameSite/Secure details follow the final dev/deployment topology.

## 3. Success response convention

Single resource:

```json
{
  "data": {}
}
```

List:

```json
{
  "data": [],
  "page": {
    "limit": 50,
    "next_cursor": null
  }
}
```

Delete without body:

```text
204 No Content
```

IDs are opaque numeric identifiers in v1.

## 4. Standard error format

```json
{
  "error": {
    "code": "IP_ALREADY_ALLOCATED",
    "message": "The IP address is already allocated.",
    "details": {}
  }
}
```

Common HTTP statuses:

- `200 OK`;
- `201 Created`;
- `204 No Content`;
- `400 Bad Request`;
- `401 Unauthorized`;
- `403 Forbidden`;
- `404 Not Found`;
- `409 Conflict`;
- `500 Internal Server Error`.

Core error codes:

```text
INVALID_REQUEST
INTERNAL_ERROR
INVALID_CIDR
SUBNET_OVERLAP
SUBNET_RESIZE_CONFLICT
SUBNET_HAS_ALLOCATIONS
IP_OUTSIDE_SUBNET
IP_ALREADY_ALLOCATED
IP_NOT_ASSIGNABLE
INTERFACE_ALREADY_ASSIGNED
VLAN_HAS_SUBNETS
VLAN_NOT_FOUND
DEVICE_NOT_FOUND
INTERFACE_NOT_FOUND
SUBNET_NOT_FOUND
IP_ALLOCATION_NOT_FOUND
MONITORING_CHECK_NOT_FOUND
MONITORING_TARGET_INVALID
AUTH_INVALID_CREDENTIALS
AUTH_REQUIRED
CSRF_INVALID_TOKEN
```

## 5. Pagination/query convention

General lists:

```text
?limit=50&cursor=<opaque>
```

**Limit contract:**

| Scenario | Behavior |
|---|---|
| `limit` omitted | default `50` applied |
| `1 ≤ limit ≤ 100` | accepted exactly as requested |
| `limit > 100` | `400 INVALID_REQUEST` |
| `limit ≤ 0` | `400 INVALID_REQUEST` |
| `limit` non-numeric | `400 INVALID_REQUEST` |

Default: **50**. Maximum: **100**. Valid range: **1..100**.

Out-of-range or malformed limit values are **never silently clamped** — they are always rejected with `400 INVALID_REQUEST` before any repository access.

The implementation may internally use ID/keyset pagination. Client code treats `cursor` as opaque.

Search/filter parameters are resource-specific.

No endpoint may enumerate an entire huge Subnet by default.

## 6. Auth endpoints

### GET `/auth/csrf`

Before authentication, returns a short-lived pre-auth CSRF token and establishes the matching CSRF cookie context. After authentication, returns/refreshes the CSRF token bound to the current session.

Example:

```json
{
  "data": {
    "csrf_token": "..."
  }
}
```

### POST `/auth/login`

Requires `X-CSRF-Token` from the pre-auth CSRF flow.

Request:

```json
{
  "username": "admin",
  "password": "..."
}
```

Success `200`:

```json
{
  "data": {
    "user": {
      "id": 1,
      "username": "admin"
    },
    "csrf_token": "..."
  }
}
```

Failure:

- `401 AUTH_INVALID_CREDENTIALS`.

### GET `/auth/session`

Returns current authenticated Administrator.

Failure:

- `401 AUTH_REQUIRED`.

### POST `/auth/logout`

Requires authenticated session + CSRF.

Success:

```text
204 No Content
```

## 7. Subnet endpoints

### GET `/subnets`

Query examples:

```text
?limit=50&cursor=...
?vlan_ref_id=5
?search=192.168.10
```

Response item:

```json
{
  "id": 10,
  "cidr": "192.168.10.0/24",
  "network": "192.168.10.0",
  "broadcast": "192.168.10.255",
  "first_usable": "192.168.10.1",
  "last_usable": "192.168.10.254",
  "usable_count": 254,
  "assigned_count": 10,
  "reserved_count": 3,
  "available_count": 241,
  "vlan_ref_id": 5,
  "description": "Lab LAN"
}
```

### POST `/subnets`

Request:

```json
{
  "cidr": "192.168.10.0/24",
  "vlan_ref_id": 5,
  "description": "Lab LAN"
}
```

Rules:

- IPv4 `/1..30`;
- CIDR must be canonical;
- global overlap rejected.

Success: `201 Created`.

Failures:

- `400 INVALID_REQUEST` (malformed/unparseable request payload);
- `400 INVALID_CIDR` (invalid or non-canonical CIDR);
- `404 VLAN_NOT_FOUND` (referenced `vlan_ref_id` does not exist);
- `409 SUBNET_OVERLAP` (overlaps with an existing subnet).

### GET `/subnets/{subnet_id}`

Returns one Subnet with derived values/counts.

### PATCH `/subnets/{subnet_id}`

Allowed fields:

```json
{
  "cidr": "192.168.10.0/25",
  "vlan_ref_id": 5,
  "description": "..."
}
```

PATCH fields are presence-aware:

- an omitted field preserves its current value;
- `cidr` must be a canonical IPv4 `/1..30` string; `null` is rejected;
- `description` accepts any string, including `""`; `null` is rejected;
- a positive integer `vlan_ref_id` sets the relationship;
- `vlan_ref_id: null` unlinks the Subnet from its VLAN.

An empty object, unknown fields, wrong field types, malformed JSON, trailing
garbage, or a second JSON value is rejected with `400 INVALID_REQUEST`.
When `cidr` is present, the request always enters the serialized Resize
transaction and obtains the Subnet advisory lock followed by the target row
lock, including when the supplied CIDR equals the current CIDR. The global
overlap query and allocation usable-range inspection run only when the
candidate CIDR differs from the locked current CIDR.

Success: `200 OK` using the single-resource envelope.

Failures:

- `400 INVALID_REQUEST` (invalid PATCH shape or field values);
- `400 INVALID_CIDR` (invalid or non-canonical CIDR);
- `404 SUBNET_NOT_FOUND` (target Subnet does not exist);
- `404 VLAN_NOT_FOUND` (supplied positive `vlan_ref_id` does not exist);
- `409 SUBNET_OVERLAP` (candidate CIDR overlaps another Subnet);
- `409 SUBNET_RESIZE_CONFLICT` (an allocation would be outside the new usable host range).

### DELETE `/subnets/{subnet_id}`

Blocked while any persisted allocation exists.

Conflict:

- `409 SUBNET_HAS_ALLOCATIONS`.

## 8. Computed Available-IP API — M0-LOCKED

### GET `/subnets/{subnet_id}/available-ips`

Computed only. Returned addresses have no allocation ID.

Query:

```text
?limit=50
&cursor=<opaque>
&range_start=192.168.10.1
&range_end=192.168.10.254
&ip=192.168.10.50
```

Rules:

- `limit` controls bounded output;
- `cursor` is opaque to the client and identifies the next bounded page;
- `next_cursor` is returned by the server;
- `range_start/range_end` optionally constrain candidate generation;
- `ip` performs exact availability lookup;
- the server must never materialize/render an entire `/1` by default.

Example response:

```json
{
  "data": [
    {
      "address": "192.168.10.21",
      "state": "available",
      "persisted": false
    }
  ],
  "page": {
    "limit": 50,
    "next_cursor": "192.168.10.70"
  }
}
```

## 9. Persisted IP Allocation endpoints

### GET `/ip-allocations`

Returns only persisted `reserved` / `assigned` rows.

Filters:

```text
?subnet_id=...
?status=reserved|assigned
?address=192.168.10.20
?interface_id=...
```

Response item:

```json
{
  "id": 100,
  "subnet_id": 10,
  "address": "192.168.10.20",
  "status": "reserved",
  "interface_id": null,
  "description": "Printer reservation"
}
```

### POST `/ip-allocations`

Creates a reservation only.

Request:

```json
{
  "subnet_id": 10,
  "address": "192.168.10.20",
  "description": "Printer reservation"
}
```

Maximum request body size: `16384 bytes`.

A request body larger than `16384 bytes` is rejected with `400 INVALID_REQUEST` before any reservation transaction begins.

Duplicate top-level JSON member names, including decoded names that differ only by JSON escaping, are rejected with `400 INVALID_REQUEST` before any reservation transaction begins.

Success:

- `201`, status=`reserved`, `interface_id=null`.

Failures:

- `400 IP_OUTSIDE_SUBNET`;
- `409 IP_ALREADY_ALLOCATED`;
- `409 IP_NOT_ASSIGNABLE` for Network/Broadcast/non-usable cases.

### DELETE `/ip-allocations/{allocation_id}`

Represents **unreserve** and is valid only for `reserved` allocation.

Success:

```text
204 No Content
```

If allocation is assigned, reject with `409 IP_NOT_ASSIGNABLE`.

## 10. Interface assignment endpoints

### PUT `/interfaces/{interface_id}/ip-assignment`

Two mutually exclusive request modes are allowed.

#### Assign an available address

```json
{
  "subnet_id": 10,
  "address": "192.168.10.30"
}
```

#### Consume a reserved allocation

```json
{
  "allocation_id": 100
}
```

Rules:

- target Interface exists;
- Interface currently has no assigned IPv4;
- allocation/address satisfies IPAM invariants;
- reserved allocation becomes assigned directly.

Success `200/201` returns the assigned allocation.

Conflicts:

- `409 IP_ALREADY_ALLOCATED`;
- `409 INTERFACE_ALREADY_ASSIGNED`;
- `409 IP_NOT_ASSIGNABLE`.

Concurrent same-IP or same-Interface requests must have at most one winner.

### DELETE `/interfaces/{interface_id}/ip-assignment`

Represents unassign.

If the assigned allocation is a Monitoring Target, the same transaction clears/reset Monitoring before release.

Success:

```text
204 No Content
```

## 11. Device endpoints

### GET `/devices`

Filters/search:

```text
?search=<device-name>
?monitoring_status=UNKNOWN|ONLINE|OFFLINE
```

### POST `/devices`

Request:

```json
{
  "name": "Router-R1",
  "type": "router",
  "location": "Lab A",
  "description": "Core lab router"
}
```

Success: `201`.

### GET `/devices/{device_id}`

Returns Device, Interfaces summary, assigned IPs and main Monitoring Check summary.

### PATCH `/devices/{device_id}`

Editable baseline fields:

- name;
- type;
- location;
- description.

### DELETE `/devices/{device_id}`

Executes explicit domain cleanup in one transaction.

Success: `204`.

## 12. Interface endpoints

### GET `/devices/{device_id}/interfaces`

Lists Interfaces owned by Device.

### POST `/devices/{device_id}/interfaces`

Request:

```json
{
  "name": "eth0",
  "mac_address": "00:11:22:33:44:55"
}
```

### GET `/interfaces/{interface_id}`

Returns Interface and current assigned IPv4 if present.

### PATCH `/interfaces/{interface_id}`

Editable:

- name;
- mac_address.

`device_id` is not editable in Phase 1.

### DELETE `/interfaces/{interface_id}`

Executes monitoring cleanup + unassign + delete when required.

## 13. VLAN endpoints

The VLAN/Subnet relationship is mandatory in the Phase-1 domain model. Full VLAN CRUD/UI is optional and must not block the IPAM/Inventory core.

### GET `/vlans`
### POST `/vlans`
### GET `/vlans/{vlan_resource_id}`
### PATCH `/vlans/{vlan_resource_id}`
### DELETE `/vlans/{vlan_resource_id}`

VLAN response item:

```json
{
  "id": 5,
  "vlan_number": 20,
  "name": "Servers"
}
```

VLAN create/update DTOs use `vlan_number` for the actual VLAN number. `id` and the `{vlan_resource_id}` path parameter identify the VLAN resource. Subnets reference that resource using `vlan_ref_id`.

Delete is blocked while any Subnet references the VLAN:

- `409 VLAN_HAS_SUBNETS`.

Exact validation of `vlan_number` (stored in database column `vlans.vlan_id`) remains OPEN before INV-06.

## 14. Monitoring endpoints

### GET `/monitoring`

Filters:

```text
?device_id=...
?status=UNKNOWN|ONLINE|OFFLINE
?enabled=true|false
```

### POST `/monitoring`

Creates the main Monitoring Check for a Device.

Request:

```json
{
  "device_id": 50,
  "target_ip_allocation_id": 200,
  "enabled": true
}
```

`target_ip_allocation_id` may be `null` only when creating a check intentionally without a selected target; such a check is `UNKNOWN` and cannot run until a valid target is selected.

Target validation:

- allocation exists;
- status=`assigned`;
- allocation has Interface;
- Interface belongs to same Device.

Success `201`.

If a target is present, state is reset and an immediate check is enqueued after commit.

### GET `/monitoring/{check_id}`

Response:

```json
{
  "data": {
    "id": 300,
    "device_id": 50,
    "target_ip_allocation_id": 200,
    "type": "ICMP",
    "enabled": true,
    "status": "UNKNOWN",
    "last_check": null,
    "last_seen": null,
    "consecutive_failures": 0
  }
}
```

### PATCH `/monitoring/{check_id}`

Editable:

- `target_ip_allocation_id`;
- `enabled`.

Target change:

- validates same-Device assigned allocation;
- resets `UNKNOWN`, counters and timestamps;
- enqueues immediate check after commit.

Setting target to `null` performs target-clear/reset semantics.

Exact `enabled=false` dashboard semantics remains OPEN before M6/M7.

### DELETE `/monitoring/{check_id}`

Deletes the Monitoring Check/configuration only. It does not unassign the target IP.

Success: `204`.

## 15. Dashboard endpoint

### GET `/dashboard`

Returns summary values such as:

```json
{
  "data": {
    "subnets": 5,
    "usable_ip": 1260,
    "assigned": 48,
    "reserved": 12,
    "available": 1200,
    "devices": 35,
    "online": 30,
    "offline": 4,
    "unknown": 1
  }
}
```

Dashboard must use persisted/derived system state. It must not perform synchronous ping-all during the GET request.

## 16. HTTP behavior matrix

| Operation | Success | Typical failure |
|---|---:|---|
| Login | 200 | 401 |
| Create resource | 201 | 400 / 409 |
| Read resource | 200 | 401 / 404 |
| Update resource | 200 | 400 / 404 / 409 |
| Delete resource | 204 | 404 / 409 |
| Authenticated write without CSRF | — | 403 |
| Duplicate IP assignment | one 200/201 | competing request 409 |

## 17. API invariants Frontend must not bypass

Frontend must never assume:

- `available` has a database ID;
- Device has a direct `ip_address`;
- any assigned IP can be selected as any Device's monitoring target;
- one failed ping means `OFFLINE`;
- backend errors can be worked around by mutating local UI state outside business rules.

## 18. Remaining API OPEN items

Not blocking M1:

1. exact VLAN number (`vlan_number`) validation payload and codes;
2. disabled-monitoring dashboard semantics;
3. split-origin vs same-origin cookie/CORS details;
4. optional advanced search/filter parameters beyond the baseline;
5. exact manual “run check now” endpoint is **not included** in v1; add only if M6 demonstrates a concrete requirement.
