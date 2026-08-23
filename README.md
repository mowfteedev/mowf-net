# MowfNet 
### The eyes of your network

**MowfNet** là hệ thống quản lý mạng, phát triển qua từng giai đoạn theo một hướng xuyên suốt:

**Know the network → Observe the network → Control the network**

Hệ thống mở rộng dần trên cùng một nền tảng.

---

## Lộ trình

| Giai đoạn | Sản phẩm | Trạng thái |
|---|---|---|
| **Phase 1** | IPAM & Network Inventory Management System | 🔵 Đang làm |
| **Phase 2** | Network Management System (NMS) | ⚪ Kế hoạch |
| **Phase 3** | Network Management & Automation Platform | ⚪ Kế hoạch |

## Phase 1 — Nền móng

Trả lời câu hỏi: *IP này ai đang giữ?*

```
Device → Interface → IP Allocation → Subnet → VLAN
```

Mỗi IP có vòng đời rõ ràng: trống → giữ chỗ → gán cho thiết bị. Không trùng, không thất lạc.

**3 khối chính:**
- **IPAM** — quản lý subnet, cấp phát IP động
- **Network Inventory** — quản lý thiết bị, interface
- **Basic Monitoring** — ping ICMP định kỳ, biết ngay thiết bị nào offline

**Stack:** Go · React · PostgreSQL · REST API · Modular Monolith

---

## Nguyên tắc

```
Correctness > Scope Discipline > Maintainability > Upgrade Path > Demo Value > Learning Value
```

Không mở rộng chỉ vì tính năng nghe hay. Không viết lại core mỗi giai đoạn.
