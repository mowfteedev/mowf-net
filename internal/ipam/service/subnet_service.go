package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
)

// SubnetDTO represents the API/Service data transfer object for Subnet with derived values.
type SubnetDTO struct {
	ID             int64  `json:"id"`
	CIDR           string `json:"cidr"`
	Network        string `json:"network"`
	Broadcast      string `json:"broadcast"`
	FirstUsable    string `json:"first_usable"`
	LastUsable     string `json:"last_usable"`
	UsableCount    int64  `json:"usable_count"`
	AssignedCount  int64  `json:"assigned_count"`
	ReservedCount  int64  `json:"reserved_count"`
	AvailableCount int64  `json:"available_count"`
	VlanRefID      *int64 `json:"vlan_ref_id"`
	Description    string `json:"description"`
}

// CreateSubnetRequest represents input to create a subnet.
type CreateSubnetRequest struct {
	CIDR        string `json:"cidr"`
	VlanRefID   *int64 `json:"vlan_ref_id"`
	Description string `json:"description"`
}

// UpdateSubnetRequest is presence-aware so PATCH can distinguish omitted,
// explicit null, and supplied values before reaching the repository.
type UpdateSubnetRequest struct {
	CIDR           *string
	CIDRSet        bool
	VlanRefID      *int64
	VlanRefIDSet   bool
	Description    *string
	DescriptionSet bool
}

// ListSubnetsRequest represents parameters for listing subnets.
type ListSubnetsRequest struct {
	VlanRefID *int64
	Search    string
	Limit     int
	Cursor    *int64
}

// PageInfo represents pagination metadata for list responses.
type PageInfo struct {
	Limit      int     `json:"limit"`
	NextCursor *string `json:"next_cursor"`
}

// ListSubnetsResponse represents the paginated result of listing subnets.
type ListSubnetsResponse struct {
	Data []*SubnetDTO `json:"data"`
	Page PageInfo     `json:"page"`
}

// SubnetService provides business operations for Subnets.
type SubnetService struct {
	repo repository.SubnetRepository
}

// NewSubnetService creates a new SubnetService.
func NewSubnetService(repo repository.SubnetRepository) *SubnetService {
	return &SubnetService{repo: repo}
}

// EncodeCursor encodes an entity ID into an opaque base64 cursor string.
func EncodeCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

// DecodeCursor decodes an opaque base64 cursor string into an entity ID.
func DecodeCursor(cursorStr string) (int64, error) {
	if strings.TrimSpace(cursorStr) == "" {
		return 0, nil
	}
	bytes, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cursorStr))
	if err != nil {
		return 0, fmt.Errorf("invalid cursor encoding: %w", err)
	}
	id, err := strconv.ParseInt(string(bytes), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid cursor value")
	}
	return id, nil
}

// ToDTO converts a domain.Subnet entity into a SubnetDTO with derived values.
func ToDTO(s *domain.Subnet, assignedCount, reservedCount int64) *SubnetDTO {
	usableCount := s.CIDR.UsableCount()
	availableCount := usableCount - assignedCount - reservedCount

	return &SubnetDTO{
		ID:             s.ID,
		CIDR:           s.CIDR.CIDR(),
		Network:        s.CIDR.Network(),
		Broadcast:      s.CIDR.Broadcast(),
		FirstUsable:    s.CIDR.FirstUsable(),
		LastUsable:     s.CIDR.LastUsable(),
		UsableCount:    usableCount,
		AssignedCount:  assignedCount,
		ReservedCount:  reservedCount,
		AvailableCount: availableCount,
		VlanRefID:      s.VlanRefID,
		Description:    s.Description,
	}
}

// CreateSubnet validates canonical CIDR, checks overlap, and persists the subnet.
func (s *SubnetService) CreateSubnet(ctx context.Context, req CreateSubnetRequest) (*SubnetDTO, error) {
	cidr, err := domain.ParseCIDR(req.CIDR)
	if err != nil {
		return nil, err
	}

	subnet := domain.NewSubnet(cidr, req.VlanRefID, strings.TrimSpace(req.Description))
	if err := s.repo.Create(ctx, &subnet); err != nil {
		return nil, err
	}

	return ToDTO(&subnet, 0, 0), nil
}

// GetSubnet retrieves a single subnet by ID with derived values.
func (s *SubnetService) GetSubnet(ctx context.Context, id int64) (*SubnetDTO, error) {
	if id <= 0 {
		return nil, domain.ErrSubnetNotFound
	}

	read, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	return ToDTO(&read.Subnet, read.AssignedCount, read.ReservedCount), nil
}

// ListSubnets queries subnets matching filter with keyset pagination.
func (s *SubnetService) ListSubnets(ctx context.Context, req ListSubnetsRequest) (*ListSubnetsResponse, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	filter := repository.ListFilter{
		VlanRefID: req.VlanRefID,
		Search:    req.Search,
		Limit:     limit,
		Cursor:    req.Cursor,
	}

	reads, nextCursorID, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	dtos := make([]*SubnetDTO, len(reads))
	for i, read := range reads {
		dtos[i] = ToDTO(&read.Subnet, read.AssignedCount, read.ReservedCount)
	}

	var nextCursorStr *string
	if nextCursorID != nil {
		encoded := EncodeCursor(*nextCursorID)
		nextCursorStr = &encoded
	}

	return &ListSubnetsResponse{
		Data: dtos,
		Page: PageInfo{
			Limit:      limit,
			NextCursor: nextCursorStr,
		},
	}, nil
}

// UpdateSubnet validates request presence/null semantics and delegates raw CIDR
// validation, the locked merge, and transaction ordering to the repository.
func (s *SubnetService) UpdateSubnet(ctx context.Context, id int64, req UpdateSubnetRequest) (*SubnetDTO, error) {
	if id <= 0 || (!req.CIDRSet && !req.VlanRefIDSet && !req.DescriptionSet) {
		return nil, domain.ErrInvalidRequest
	}

	patch := repository.UpdateSubnet{
		CIDRSet:        req.CIDRSet,
		VlanRefID:      req.VlanRefID,
		VlanRefIDSet:   req.VlanRefIDSet,
		DescriptionSet: req.DescriptionSet,
	}
	if req.CIDRSet {
		if req.CIDR == nil {
			return nil, domain.ErrInvalidRequest
		}
		patch.CIDR = *req.CIDR
	}
	if req.DescriptionSet {
		if req.Description == nil {
			return nil, domain.ErrInvalidRequest
		}
		patch.Description = *req.Description
	}
	if req.VlanRefIDSet && req.VlanRefID != nil && *req.VlanRefID <= 0 {
		return nil, domain.ErrInvalidRequest
	}

	read, err := s.repo.Update(ctx, id, patch)
	if err != nil {
		return nil, err
	}
	return ToDTO(&read.Subnet, read.AssignedCount, read.ReservedCount), nil
}

func (s *SubnetService) DeleteSubnet(ctx context.Context, id int64) error {
	if id <= 0 {
		return domain.ErrInvalidRequest
	}
	return s.repo.Delete(ctx, id)
}
