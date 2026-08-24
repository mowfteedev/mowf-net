package service

import (
	"context"
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

// SubnetService provides business operations for Subnets.
type SubnetService struct {
	repo repository.SubnetRepository
}

// NewSubnetService creates a new SubnetService.
func NewSubnetService(repo repository.SubnetRepository) *SubnetService {
	return &SubnetService{repo: repo}
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

	usableCount := subnet.CIDR.UsableCount()
	return &SubnetDTO{
		ID:             subnet.ID,
		CIDR:           subnet.CIDR.CIDR(),
		Network:        subnet.CIDR.Network(),
		Broadcast:      subnet.CIDR.Broadcast(),
		FirstUsable:    subnet.CIDR.FirstUsable(),
		LastUsable:     subnet.CIDR.LastUsable(),
		UsableCount:    usableCount,
		AssignedCount:  0,
		ReservedCount:  0,
		AvailableCount: usableCount,
		VlanRefID:      subnet.VlanRefID,
		Description:    subnet.Description,
	}, nil
}
