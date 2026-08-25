package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"strings"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
)

// AllocationRepository reads persisted IP allocation rows from PostgreSQL.
type AllocationRepository struct {
	db *sql.DB
}

func NewAllocationRepository(db *sql.DB) *AllocationRepository {
	return &AllocationRepository{db: db}
}

// ListOccupiedAddresses returns all persisted reserved or assigned addresses
// in one bounded, inclusive window. INET host-form /32 ordering is numeric for
// the IPv4-only rows enforced by the schema.
func (r *AllocationRepository) ListOccupiedAddresses(ctx context.Context, subnetID int64, start, end netip.Addr) ([]netip.Addr, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT host(address)
		FROM ip_allocations
		WHERE subnet_id = $1
		  AND address >= $2::inet
		  AND address <= $3::inet
		ORDER BY address ASC
	`, subnetID, start.String(), end.String())
	if err != nil {
		return nil, fmt.Errorf("failed to query occupied allocation addresses: %w", err)
	}
	defer rows.Close()

	addresses := make([]netip.Addr, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("failed to scan occupied allocation address: %w", err)
		}
		address, err := netip.ParseAddr(raw)
		if err != nil || !address.Is4() {
			return nil, fmt.Errorf("corrupted occupied allocation address %q", raw)
		}
		addresses = append(addresses, address)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate occupied allocation addresses: %w", err)
	}
	return addresses, nil
}

// List returns one stable, ID-ordered page. It reads limit+1 rows so the
// continuation cursor can be determined without a second query.
func (r *AllocationRepository) List(ctx context.Context, filter repository.AllocationListFilter) ([]*domain.Allocation, *int64, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	var whereClauses []string
	var args []any
	argIndex := 1
	if filter.Cursor != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("id > $%d", argIndex))
		args = append(args, *filter.Cursor)
		argIndex++
	}
	if filter.SubnetID != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("subnet_id = $%d", argIndex))
		args = append(args, *filter.SubnetID)
		argIndex++
	}
	if filter.Status != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("status = $%d", argIndex))
		args = append(args, string(*filter.Status))
		argIndex++
	}
	if filter.Address != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("address = $%d::inet", argIndex))
		args = append(args, filter.Address.String())
		argIndex++
	}
	if filter.InterfaceID != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("interface_id = $%d", argIndex))
		args = append(args, *filter.InterfaceID)
		argIndex++
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}
	query := fmt.Sprintf(`
		SELECT id, subnet_id, host(address), status, interface_id, description, created_at, updated_at
		FROM ip_allocations
		%s
		ORDER BY id ASC
		LIMIT $%d
	`, whereSQL, argIndex)
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query allocation list: %w", err)
	}
	defer rows.Close()

	allocations := make([]*domain.Allocation, 0, limit+1)
	for rows.Next() {
		allocation, err := scanAllocation(rows.Scan)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to scan allocation row: %w", err)
		}
		allocations = append(allocations, allocation)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to iterate allocation rows: %w", err)
	}

	var nextCursor *int64
	if len(allocations) > limit {
		cursor := allocations[limit-1].ID
		nextCursor = &cursor
		allocations = allocations[:limit]
	}
	return allocations, nextCursor, nil
}

type allocationScanner func(dest ...any) error

func scanAllocation(scan allocationScanner) (*domain.Allocation, error) {
	var allocation domain.Allocation
	var address string
	var interfaceID sql.NullInt64
	if err := scan(
		&allocation.ID,
		&allocation.SubnetID,
		&address,
		&allocation.Status,
		&interfaceID,
		&allocation.Description,
		&allocation.CreatedAt,
		&allocation.UpdatedAt,
	); err != nil {
		return nil, err
	}

	parsedAddress, err := netip.ParseAddr(address)
	if err != nil || !parsedAddress.Is4() {
		return nil, fmt.Errorf("corrupted allocation address %q", address)
	}
	allocation.Address = parsedAddress
	if interfaceID.Valid {
		allocation.InterfaceID = &interfaceID.Int64
	}
	return &allocation, nil
}
