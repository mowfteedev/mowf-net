package repository

import (
	"context"
	"net/netip"
)

// SubnetReader is the narrow read capability needed by available-IP queries.
type SubnetReader interface {
	GetByID(ctx context.Context, id int64) (*SubnetRead, error)
}

// OccupiedAddressRepository reads persisted occupied addresses from one
// bounded, inclusive IPv4 window.
type OccupiedAddressRepository interface {
	ListOccupiedAddresses(ctx context.Context, subnetID int64, start, end netip.Addr) ([]netip.Addr, error)
}
