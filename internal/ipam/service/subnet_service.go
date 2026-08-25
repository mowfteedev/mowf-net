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
	if availableCount < 0 {
		availableCount = 0
	}

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

	subnet, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	return ToDTO(subnet, 0, 0), nil
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

	subnets, nextCursorID, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	dtos := make([]*SubnetDTO, len(subnets))
	for i, sub := range subnets {
		// TODO(M2): transitional M1 behavior — Tech-Lead-authorized.
		// assigned_count and reserved_count are hardcoded to 0 because ip_allocations
		// persistence does not yet exist in M1. available_count therefore equals usable_count.
		// M2 MUST replace these zero values with actual DB aggregates:
		//   assigned_count  = COUNT(*) WHERE subnet_id=sub.ID AND status='assigned'
		//   reserved_count  = COUNT(*) WHERE subnet_id=sub.ID AND status='reserved'
		//   available_count = usable_count - assigned_count - reserved_count
		// M2 MUST NOT close until GET /subnets and GET /subnets/{id} use these real counts.
		dtos[i] = ToDTO(sub, 0, 0)
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
