package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"
	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
)

// SubnetCoordinationKey is the global advisory transaction lock key (0x4D4F57465355424E) used to serialize subnet create and resize operations.
const SubnetCoordinationKey int64 = 0x4D4F57465355424E

// ListFilter aliases repository.ListFilter.
type ListFilter = repository.ListFilter

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

// GetByID retrieves a single subnet by its ID and reconstructs its domain CIDR.
func (r *SubnetRepository) GetByID(ctx context.Context, id int64) (*domain.Subnet, error) {
	query := `
		SELECT id, vlan_ref_id, host(network), prefix_length, description, created_at, updated_at
		FROM subnets
		WHERE id = $1
	`
	var (
		subnetID     int64
		vlanRefID    sql.NullInt64
		networkIP    string
		prefixLength int
		description  string
		createdAt    sql.NullTime
		updatedAt    sql.NullTime
	)

	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&subnetID,
		&vlanRefID,
		&networkIP,
		&prefixLength,
		&description,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrSubnetNotFound
		}
		return nil, fmt.Errorf("failed to query subnet by id: %w", err)
	}

	cidr, err := domain.NewCIDRFromParts(networkIP, prefixLength)
	if err != nil {
		return nil, fmt.Errorf("corrupted subnet record in database: %w", err)
	}

	subnet := &domain.Subnet{
		ID:          subnetID,
		CIDR:        cidr,
		Description: description,
		CreatedAt:   createdAt.Time,
		UpdatedAt:   updatedAt.Time,
	}
	if vlanRefID.Valid {
		subnet.VlanRefID = &vlanRefID.Int64
	}

	return subnet, nil
}

// List queries subnets matching filter with keyset pagination.
func (r *SubnetRepository) List(ctx context.Context, filter repository.ListFilter) ([]*domain.Subnet, *int64, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	var (
		whereClauses []string
		args         []any
		argIdx       = 1
	)

	if filter.Cursor != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("id > $%d", argIdx))
		args = append(args, *filter.Cursor)
		argIdx++
	}

	if filter.VlanRefID != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("vlan_ref_id = $%d", argIdx))
		args = append(args, *filter.VlanRefID)
		argIdx++
	}

	if strings.TrimSpace(filter.Search) != "" {
		searchTerm := "%" + strings.TrimSpace(filter.Search) + "%"
		whereClauses = append(whereClauses, fmt.Sprintf(
			"(host(network) ILIKE $%d OR (host(network) || '/' || prefix_length::text) ILIKE $%d OR description ILIKE $%d)",
			argIdx, argIdx, argIdx,
		))
		args = append(args, searchTerm)
		argIdx++
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT id, vlan_ref_id, host(network), prefix_length, description, created_at, updated_at
		FROM subnets
		%s
		ORDER BY id ASC
		LIMIT $%d
	`, whereSQL, argIdx)
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query subnets list: %w", err)
	}
	defer rows.Close()

	var subnets []*domain.Subnet
	for rows.Next() {
		var (
			subnetID     int64
			vlanRefID    sql.NullInt64
			networkIP    string
			prefixLength int
			description  string
			createdAt    sql.NullTime
			updatedAt    sql.NullTime
		)

		if err := rows.Scan(&subnetID, &vlanRefID, &networkIP, &prefixLength, &description, &createdAt, &updatedAt); err != nil {
			return nil, nil, fmt.Errorf("failed to scan subnet row: %w", err)
		}

		cidr, err := domain.NewCIDRFromParts(networkIP, prefixLength)
		if err != nil {
			return nil, nil, fmt.Errorf("corrupted subnet record in list: %w", err)
		}

		s := &domain.Subnet{
			ID:          subnetID,
			CIDR:        cidr,
			Description: description,
			CreatedAt:   createdAt.Time,
			UpdatedAt:   updatedAt.Time,
		}
		if vlanRefID.Valid {
			s.VlanRefID = &vlanRefID.Int64
		}

		subnets = append(subnets, s)
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to iterate subnet rows: %w", err)
	}

	var nextCursor *int64
	if len(subnets) > limit {
		nextCursor = &subnets[limit-1].ID
		subnets = subnets[:limit]
	}

	return subnets, nextCursor, nil
}
