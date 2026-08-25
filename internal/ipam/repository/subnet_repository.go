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

// SubnetRead combines the authoritative Subnet entity with persisted allocation counts.
// Counts are a read concern and intentionally are not fields on domain.Subnet.
type SubnetRead struct {
	domain.Subnet
	AssignedCount int64
	ReservedCount int64
}

// UpdateSubnet contains a presence-aware partial Subnet update. CIDR has already
// passed domain validation, while merging is performed against the locked row.
type UpdateSubnet struct {
	CIDR           *domain.CIDR
	CIDRSet        bool
	VlanRefID      *int64
	VlanRefIDSet   bool
	Description    string
	DescriptionSet bool
}

// SubnetRepository defines the persistence interface for Subnet entities.
type SubnetRepository interface {
	// Create persists a new subnet inside an advisory-locked transaction with global overlap validation.
	Create(ctx context.Context, subnet *domain.Subnet) error

	// GetByID retrieves a single subnet by its ID, reconstructing its domain CIDR.
	GetByID(ctx context.Context, id int64) (*SubnetRead, error)

	// List queries subnets matching the filter with keyset pagination.
	List(ctx context.Context, filter ListFilter) ([]*SubnetRead, *int64, error)

	// Update merges a partial update against a row locked inside the transaction.
	Update(ctx context.Context, id int64, patch UpdateSubnet) (*SubnetRead, error)

	// Delete removes a subnet only when it has no persisted allocations.
	Delete(ctx context.Context, id int64) error
}
