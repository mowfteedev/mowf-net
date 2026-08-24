package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"
	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
)

// SubnetCoordinationKey is the global advisory transaction lock key for serializing subnet create and resize operations.
const SubnetCoordinationKey int64 = 0x4D4F57465355424E // ASCII for "MOWFSUB"

// SubnetRepository implements repository.SubnetRepository using PostgreSQL.
type SubnetRepository struct {
	db *sql.DB
}

// NewSubnetRepository creates a new SubnetRepository.
func NewSubnetRepository(db *sql.DB) *SubnetRepository {
	return &SubnetRepository{db: db}
}

// Create persists a new subnet inside an advisory-locked transaction with global overlap validation.
func (r *SubnetRepository) Create(ctx context.Context, subnet *domain.Subnet) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Acquire transaction-scoped advisory lock to serialize concurrent create/resize operations
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", SubnetCoordinationKey); err != nil {
		return fmt.Errorf("failed to acquire subnet advisory lock: %w", err)
	}

	// 2. Query global overlap against all existing subnets in PostgreSQL
	overlapQuery := `
		SELECT EXISTS(
			SELECT 1 FROM subnets
			WHERE set_masklen(network, prefix_length) && set_masklen($1::inet, $2::smallint)
		)
	`
	var overlaps bool
	err = tx.QueryRowContext(ctx, overlapQuery, subnet.CIDR.Network(), subnet.CIDR.PrefixLength()).Scan(&overlaps)
	if err != nil {
		return fmt.Errorf("failed to query subnet overlap: %w", err)
	}
	if overlaps {
		return domain.ErrSubnetOverlap
	}

	// 3. Insert subnet
	insertQuery := `
		INSERT INTO subnets (vlan_ref_id, network, prefix_length, description, created_at, updated_at)
		VALUES ($1, $2::inet, $3, $4, NOW(), NOW())
		RETURNING id, created_at, updated_at
	`
	err = tx.QueryRowContext(ctx, insertQuery,
		subnet.VlanRefID,
		subnet.CIDR.Network(),
		subnet.CIDR.PrefixLength(),
		subnet.Description,
	).Scan(&subnet.ID, &subnet.CreatedAt, &subnet.UpdatedAt)

	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) {
			switch pqErr.Code {
			case "23505": // unique_violation
				return domain.ErrSubnetOverlap
			case "23503": // foreign_key_violation
				if pqErr.Constraint == "subnets_vlan_ref_id_fkey" {
					return domain.ErrVlanNotFound
				}
			case "23514": // check_violation
				return domain.ErrInvalidCIDR
			}
		}
		return fmt.Errorf("failed to insert subnet: %w", err)
	}

	// 4. Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}
