package repository

import (
	"context"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
)

// SubnetRepository defines the persistence interface for Subnet entities.
type SubnetRepository interface {
	// Create persists a new subnet inside an advisory-locked transaction with global overlap validation.
	Create(ctx context.Context, subnet *domain.Subnet) error
}
