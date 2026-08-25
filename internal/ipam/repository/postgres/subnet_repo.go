package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/lib/pq"
	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
)

// SubnetCoordinationKey serializes global subnet create and resize checks.
const SubnetCoordinationKey int64 = 0x4D4F57465355424E

type ListFilter = repository.ListFilter

type SubnetRepository struct {
	db *sql.DB
}

func NewSubnetRepository(db *sql.DB) *SubnetRepository {
	return &SubnetRepository{db: db}
}

func (r *SubnetRepository) Create(ctx context.Context, subnet *domain.Subnet) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", SubnetCoordinationKey); err != nil {
		return fmt.Errorf("failed to acquire subnet advisory lock: %w", err)
	}

	var overlaps bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM subnets
			WHERE set_masklen(network, prefix_length) && set_masklen($1::inet, $2::smallint)
		)
	`, subnet.CIDR.Network(), subnet.CIDR.PrefixLength()).Scan(&overlaps)
	if err != nil {
		return fmt.Errorf("failed to query subnet overlap: %w", err)
	}
	if overlaps {
		return domain.ErrSubnetOverlap
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO subnets (vlan_ref_id, network, prefix_length, description, created_at, updated_at)
		VALUES ($1, $2::inet, $3, $4, NOW(), NOW())
		RETURNING id, created_at, updated_at
	`, subnet.VlanRefID, subnet.CIDR.Network(), subnet.CIDR.PrefixLength(), subnet.Description).
		Scan(&subnet.ID, &subnet.CreatedAt, &subnet.UpdatedAt)
	if err != nil {
		return classifySubnetWriteError(err, "failed to insert subnet")
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

func (r *SubnetRepository) GetByID(ctx context.Context, id int64) (*repository.SubnetRead, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT
			s.id, s.vlan_ref_id, host(s.network), s.prefix_length, s.description,
			s.created_at, s.updated_at,
			COUNT(a.id) FILTER (WHERE a.status = 'assigned'),
			COUNT(a.id) FILTER (WHERE a.status = 'reserved')
		FROM subnets s
		LEFT JOIN ip_allocations a ON a.subnet_id = s.id
		WHERE s.id = $1
		GROUP BY s.id
	`, id)

	read, err := scanSubnetRead(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrSubnetNotFound
		}
		return nil, fmt.Errorf("failed to query subnet by id: %w", err)
	}
	return read, nil
}

// List selects a bounded page before joining allocations, so counts use one
// bounded SQL query and never degrade into an N+1 query pattern.
func (r *SubnetRepository) List(ctx context.Context, filter repository.ListFilter) ([]*repository.SubnetRead, *int64, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	var whereClauses []string
	var args []any
	argIdx := 1
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
		whereClauses = append(whereClauses, fmt.Sprintf(
			"(host(network) ILIKE $%d OR (host(network) || '/' || prefix_length::text) ILIKE $%d OR description ILIKE $%d)",
			argIdx, argIdx, argIdx,
		))
		args = append(args, "%"+strings.TrimSpace(filter.Search)+"%")
		argIdx++
	}
	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	query := fmt.Sprintf(`
		WITH page AS (
			SELECT id, vlan_ref_id, network, prefix_length, description, created_at, updated_at
			FROM subnets
			%s
			ORDER BY id ASC
			LIMIT $%d
		)
		SELECT
			page.id, page.vlan_ref_id, host(page.network), page.prefix_length,
			page.description, page.created_at, page.updated_at,
			COUNT(a.id) FILTER (WHERE a.status = 'assigned'),
			COUNT(a.id) FILTER (WHERE a.status = 'reserved')
		FROM page
		LEFT JOIN ip_allocations a ON a.subnet_id = page.id
		GROUP BY page.id, page.vlan_ref_id, page.network, page.prefix_length,
			page.description, page.created_at, page.updated_at
		ORDER BY page.id ASC
	`, whereSQL, argIdx)
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query subnets list: %w", err)
	}
	defer rows.Close()

	var reads []*repository.SubnetRead
	for rows.Next() {
		read, err := scanSubnetRead(rows.Scan)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to scan subnet row: %w", err)
		}
		reads = append(reads, read)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to iterate subnet rows: %w", err)
	}

	var nextCursor *int64
	if len(reads) > limit {
		cursor := reads[limit-1].ID
		nextCursor = &cursor
		reads = reads[:limit]
	}
	return reads, nextCursor, nil
}

func (r *SubnetRepository) Update(ctx context.Context, id int64, patch repository.UpdateSubnet) (*repository.SubnetRead, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin subnet update transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Presence, not difference, selects the resize path. This prevents a
	// target-row-lock -> advisory-lock inversion on same-CIDR requests.
	if patch.CIDRSet {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", SubnetCoordinationKey); err != nil {
			return nil, fmt.Errorf("failed to acquire subnet advisory lock: %w", err)
		}
	}

	current, err := getSubnetForUpdate(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	candidate := current.Subnet
	if patch.CIDRSet {
		candidate.CIDR = *patch.CIDR
	}
	if patch.VlanRefIDSet {
		candidate.VlanRefID = patch.VlanRefID
	}
	if patch.DescriptionSet {
		candidate.Description = patch.Description
	}

	if patch.VlanRefIDSet && patch.VlanRefID != nil {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM vlans WHERE id = $1)", *patch.VlanRefID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("failed to validate VLAN reference: %w", err)
		}
		if !exists {
			return nil, domain.ErrVlanNotFound
		}
	}

	cidrChanged := candidate.CIDR.CIDR() != current.CIDR.CIDR()
	if cidrChanged {
		var overlaps bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM subnets
				WHERE id <> $1
				  AND set_masklen(network, prefix_length) && set_masklen($2::inet, $3::smallint)
			)
		`, id, candidate.CIDR.Network(), candidate.CIDR.PrefixLength()).Scan(&overlaps); err != nil {
			return nil, fmt.Errorf("failed to query subnet resize overlap: %w", err)
		}
		if overlaps {
			return nil, domain.ErrSubnetOverlap
		}

		rows, err := tx.QueryContext(ctx, "SELECT host(address) FROM ip_allocations WHERE subnet_id = $1 ORDER BY id", id)
		if err != nil {
			return nil, fmt.Errorf("failed to inspect subnet allocations: %w", err)
		}
		for rows.Next() {
			var address string
			if err := rows.Scan(&address); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("failed to scan subnet allocation: %w", err)
			}
			addr, err := netip.ParseAddr(address)
			if err != nil || !candidate.CIDR.IsUsable(addr) {
				_ = rows.Close()
				return nil, domain.ErrSubnetResizeConflict
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("failed to iterate subnet allocations: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("failed to close subnet allocation rows: %w", err)
		}
	}

	if err := tx.QueryRowContext(ctx, `
		UPDATE subnets
		SET vlan_ref_id = $2, network = $3::inet, prefix_length = $4,
			description = $5, updated_at = NOW()
		WHERE id = $1
		RETURNING created_at, updated_at
	`, id, candidate.VlanRefID, candidate.CIDR.Network(), candidate.CIDR.PrefixLength(), candidate.Description).
		Scan(&candidate.CreatedAt, &candidate.UpdatedAt); err != nil {
		return nil, classifySubnetWriteError(err, "failed to update subnet")
	}

	read := &repository.SubnetRead{Subnet: candidate}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE status = 'assigned'),
		       COUNT(*) FILTER (WHERE status = 'reserved')
		FROM ip_allocations WHERE subnet_id = $1
	`, id).Scan(&read.AssignedCount, &read.ReservedCount); err != nil {
		return nil, fmt.Errorf("failed to count subnet allocations after update: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit subnet update transaction: %w", err)
	}
	return read, nil
}

func (r *SubnetRepository) Delete(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin subnet delete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var lockedID int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM subnets WHERE id = $1 FOR UPDATE", id).Scan(&lockedID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrSubnetNotFound
		}
		return fmt.Errorf("failed to lock subnet for delete: %w", err)
	}

	var hasAllocations bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM ip_allocations WHERE subnet_id = $1)", id).Scan(&hasAllocations); err != nil {
		return fmt.Errorf("failed to check subnet allocations before delete: %w", err)
	}
	if hasAllocations {
		return domain.ErrSubnetHasAllocations
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM subnets WHERE id = $1", id); err != nil {
		return classifySubnetDeleteError(err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit subnet delete transaction: %w", err)
	}
	return nil
}

type scanner func(dest ...any) error

func scanSubnetRead(scan scanner) (*repository.SubnetRead, error) {
	var subnetID int64
	var vlanRefID sql.NullInt64
	var networkIP string
	var prefixLength int
	var description string
	var createdAt, updatedAt sql.NullTime
	var assigned, reserved int64
	if err := scan(&subnetID, &vlanRefID, &networkIP, &prefixLength, &description, &createdAt, &updatedAt, &assigned, &reserved); err != nil {
		return nil, err
	}
	cidr, err := domain.NewCIDRFromParts(networkIP, prefixLength)
	if err != nil {
		return nil, fmt.Errorf("corrupted subnet record in database: %w", err)
	}
	read := &repository.SubnetRead{
		Subnet: domain.Subnet{
			ID: subnetID, CIDR: cidr, Description: description,
			CreatedAt: createdAt.Time, UpdatedAt: updatedAt.Time,
		},
		AssignedCount: assigned,
		ReservedCount: reserved,
	}
	if vlanRefID.Valid {
		read.VlanRefID = &vlanRefID.Int64
	}
	return read, nil
}

func getSubnetForUpdate(ctx context.Context, tx *sql.Tx, id int64) (*repository.SubnetRead, error) {
	var subnetID int64
	var vlanRefID sql.NullInt64
	var networkIP string
	var prefixLength int
	var description string
	var createdAt, updatedAt sql.NullTime
	err := tx.QueryRowContext(ctx, `
		SELECT id, vlan_ref_id, host(network), prefix_length, description, created_at, updated_at
		FROM subnets WHERE id = $1 FOR UPDATE
	`, id).Scan(&subnetID, &vlanRefID, &networkIP, &prefixLength, &description, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrSubnetNotFound
		}
		return nil, fmt.Errorf("failed to lock subnet for update: %w", err)
	}
	cidr, err := domain.NewCIDRFromParts(networkIP, prefixLength)
	if err != nil {
		return nil, fmt.Errorf("corrupted locked subnet record: %w", err)
	}
	read := &repository.SubnetRead{Subnet: domain.Subnet{
		ID: subnetID, CIDR: cidr, Description: description,
		CreatedAt: createdAt.Time, UpdatedAt: updatedAt.Time,
	}}
	if vlanRefID.Valid {
		read.VlanRefID = &vlanRefID.Int64
	}
	return read, nil
}

func classifySubnetWriteError(err error, contextMessage string) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch pqErr.Code {
		case "23505":
			return domain.ErrSubnetOverlap
		case "23503":
			if pqErr.Constraint == "subnets_vlan_ref_id_fkey" {
				return domain.ErrVlanNotFound
			}
		case "23514":
			return domain.ErrInvalidCIDR
		}
	}
	return fmt.Errorf("%s: %w", contextMessage, err)
}

// Delete FK classification deliberately requires both the SQLSTATE and the
// exact ip_allocations FK name; unrelated FK failures stay internal errors.
func classifySubnetDeleteError(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23503" && pqErr.Constraint == "ip_allocations_subnet_id_fkey" {
		return domain.ErrSubnetHasAllocations
	}
	return fmt.Errorf("failed to delete subnet: %w", err)
}
