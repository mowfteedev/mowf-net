package repository

import (
	"context"
	"net/netip"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
)

// AllocationListFilter defines typed filtering and keyset pagination options
// for persisted IP allocations.
type AllocationListFilter struct {
	SubnetID    *int64
	Status      *domain.AllocationStatus
	Address     *netip.Addr
	InterfaceID *int64
	Limit       int
	Cursor      *int64
}

// AllocationReservationTransaction exposes only the operations needed to
// reserve an address while the current Subnet row remains locked.
type AllocationReservationTransaction interface {
	LockSubnet(ctx context.Context, subnetID int64) (domain.CIDR, error)
	InsertReserved(ctx context.Context, allocation *domain.Allocation) error
	Commit() error
	Rollback() error
}

// AllocationRepository defines persisted IP allocation access.
type AllocationRepository interface {
	List(ctx context.Context, filter AllocationListFilter) ([]*domain.Allocation, *int64, error)
	BeginReservation(ctx context.Context) (AllocationReservationTransaction, error)
}
