package service

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
)

// AllocationDTO is the API representation of a persisted IP allocation.
type AllocationDTO struct {
	ID          int64  `json:"id"`
	SubnetID    int64  `json:"subnet_id"`
	Address     string `json:"address"`
	Status      string `json:"status"`
	InterfaceID *int64 `json:"interface_id"`
	Description string `json:"description"`
}

// ListAllocationsRequest contains validated typed allocation list parameters.
type ListAllocationsRequest struct {
	SubnetID    *int64
	Status      *domain.AllocationStatus
	Address     *netip.Addr
	InterfaceID *int64
	Limit       int
	Cursor      *int64
}

// ListAllocationsResponse is the standard API list envelope for allocations.
type ListAllocationsResponse struct {
	Data []*AllocationDTO `json:"data"`
	Page PageInfo         `json:"page"`
}

// ReserveAllocationRequest contains the validated input for a reservation.
type ReserveAllocationRequest struct {
	SubnetID    int64
	Address     netip.Addr
	Description string
}

// AllocationService provides persisted allocation operations.
type AllocationService struct {
	repo repository.AllocationRepository
}

// ReserveAllocation transitions one derived available address to a persisted
// reserved allocation. Membership and usability are evaluated only after the
// repository has locked and returned the current Subnet CIDR.
func (s *AllocationService) ReserveAllocation(ctx context.Context, req ReserveAllocationRequest) (*AllocationDTO, error) {
	if req.SubnetID <= 0 || !req.Address.IsValid() || !req.Address.Is4() {
		return nil, domain.ErrInvalidRequest
	}

	tx, err := s.repo.BeginReservation(ctx)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	lockedCIDR, err := tx.LockSubnet(ctx, req.SubnetID)
	if err != nil {
		return nil, err
	}
	if !lockedCIDR.Contains(req.Address) {
		return nil, domain.ErrIPOutsideSubnet
	}
	if !lockedCIDR.IsUsable(req.Address) {
		return nil, domain.ErrIPNotAssignable
	}

	allocation := &domain.Allocation{
		SubnetID:    req.SubnetID,
		Address:     req.Address,
		Status:      domain.AllocationStatusReserved,
		InterfaceID: nil,
		Description: req.Description,
	}
	if err := tx.InsertReserved(ctx, allocation); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return allocationToDTO(allocation), nil
}

// UnreserveAllocation transitions one persisted reserved allocation back to
// derived availability by deleting it in the same transaction that locks and
// validates its current state.
func (s *AllocationService) UnreserveAllocation(ctx context.Context, allocationID int64) error {
	if allocationID <= 0 {
		return domain.ErrInvalidRequest
	}

	tx, err := s.repo.BeginUnreservation(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	allocation, err := tx.LockAllocation(ctx, allocationID)
	if err != nil {
		return err
	}
	switch allocation.Status {
	case domain.AllocationStatusAssigned:
		return domain.ErrIPNotAssignable
	case domain.AllocationStatusReserved:
		// Continue with the only state transition authorized by M2-D.
	default:
		return fmt.Errorf("unexpected locked allocation status %q", allocation.Status)
	}

	if err := tx.DeleteLockedAllocation(ctx, allocation.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func NewAllocationService(repo repository.AllocationRepository) *AllocationService {
	return &AllocationService{repo: repo}
}

// ListAllocations lists persisted reserved and assigned allocation rows.
func (s *AllocationService) ListAllocations(ctx context.Context, req ListAllocationsRequest) (*ListAllocationsResponse, error) {
	limit := req.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return nil, domain.ErrInvalidRequest
	}

	allocations, nextCursorID, err := s.repo.List(ctx, repository.AllocationListFilter{
		SubnetID:    req.SubnetID,
		Status:      req.Status,
		Address:     req.Address,
		InterfaceID: req.InterfaceID,
		Limit:       limit,
		Cursor:      req.Cursor,
	})
	if err != nil {
		return nil, err
	}

	dtos := make([]*AllocationDTO, len(allocations))
	for i, allocation := range allocations {
		dtos[i] = allocationToDTO(allocation)
	}

	var nextCursor *string
	if nextCursorID != nil {
		encoded := EncodeCursor(*nextCursorID)
		nextCursor = &encoded
	}
	return &ListAllocationsResponse{
		Data: dtos,
		Page: PageInfo{Limit: limit, NextCursor: nextCursor},
	}, nil
}

func allocationToDTO(allocation *domain.Allocation) *AllocationDTO {
	return &AllocationDTO{
		ID:          allocation.ID,
		SubnetID:    allocation.SubnetID,
		Address:     allocation.Address.String(),
		Status:      string(allocation.Status),
		InterfaceID: allocation.InterfaceID,
		Description: allocation.Description,
	}
}
