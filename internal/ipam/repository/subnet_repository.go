package repository

import (
	"context"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
)

// ListFilter defines parameters for filtering and paginating subnets.
type ListFilter struct {
	VlanRefID *int64
	Search    string
	Limit     int
	Cursor    *int64
}

// SubnetRepository defines the persistence interface for Subnet entities.
type SubnetRepository interface {
	// Create persists a new subnet inside an advisory-locked transaction with global overlap validation.
	Create(ctx context.Context, subnet *domain.Subnet) error

	// GetByID retrieves a single subnet by its ID, reconstructing its domain CIDR.
	GetByID(ctx context.Context, id int64) (*domain.Subnet, error)

	// List queries subnets matching the filter with keyset pagination.
	List(ctx context.Context, filter ListFilter) ([]*domain.Subnet, *int64, error)
}
