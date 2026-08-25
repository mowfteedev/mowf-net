package domain

import (
	"net/netip"
	"time"
)

// AllocationStatus identifies a persisted IP allocation state.
type AllocationStatus string

const (
	AllocationStatusReserved AllocationStatus = "reserved"
	AllocationStatusAssigned AllocationStatus = "assigned"
)

// Allocation represents a persisted occupied IPv4 address. Available addresses
// are derived and deliberately have no Allocation entity.
type Allocation struct {
	ID          int64
	SubnetID    int64
	Address     netip.Addr
	Status      AllocationStatus
	InterfaceID *int64
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
