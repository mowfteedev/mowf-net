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

// AllocationRepository defines read access for persisted IP allocations.
type AllocationRepository interface {
	List(ctx context.Context, filter AllocationListFilter) ([]*domain.Allocation, *int64, error)
}
