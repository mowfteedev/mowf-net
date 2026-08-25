package service

import (
	"context"
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

// AllocationService provides allocation read operations.
type AllocationService struct {
	repo repository.AllocationRepository
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
