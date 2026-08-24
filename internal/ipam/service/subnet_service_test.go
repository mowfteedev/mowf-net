package service

import (
	"context"
	"errors"
	"testing"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
)

type mockSubnetRepo struct {
	createFn func(ctx context.Context, subnet *domain.Subnet) error
}

func (m *mockSubnetRepo) Create(ctx context.Context, subnet *domain.Subnet) error {
	if m.createFn != nil {
		return m.createFn(ctx, subnet)
	}
	subnet.ID = 1
	return nil
}

func TestSubnetService_CreateSubnet_Valid(t *testing.T) {
	repo := &mockSubnetRepo{
		createFn: func(ctx context.Context, subnet *domain.Subnet) error {
			subnet.ID = 42
			return nil
		},
	}
	svc := NewSubnetService(repo)

	req := CreateSubnetRequest{
		CIDR:        "192.168.10.0/24",
		Description: "  Lab LAN  ",
	}

	dto, err := svc.CreateSubnet(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if dto.ID != 42 {
		t.Errorf("ID = %d, want 42", dto.ID)
	}
	if dto.CIDR != "192.168.10.0/24" {
		t.Errorf("CIDR = %s, want 192.168.10.0/24", dto.CIDR)
	}
	if dto.Network != "192.168.10.0" {
		t.Errorf("Network = %s, want 192.168.10.0", dto.Network)
	}
	if dto.Broadcast != "192.168.10.255" {
		t.Errorf("Broadcast = %s, want 192.168.10.255", dto.Broadcast)
	}
	if dto.FirstUsable != "192.168.10.1" {
		t.Errorf("FirstUsable = %s, want 192.168.10.1", dto.FirstUsable)
	}
	if dto.LastUsable != "192.168.10.254" {
		t.Errorf("LastUsable = %s, want 192.168.10.254", dto.LastUsable)
	}
	if dto.UsableCount != 254 {
		t.Errorf("UsableCount = %d, want 254", dto.UsableCount)
	}
	if dto.AssignedCount != 0 {
		t.Errorf("AssignedCount = %d, want 0", dto.AssignedCount)
	}
	if dto.ReservedCount != 0 {
		t.Errorf("ReservedCount = %d, want 0", dto.ReservedCount)
	}
	if dto.AvailableCount != 254 {
		t.Errorf("AvailableCount = %d, want 254", dto.AvailableCount)
	}
	if dto.Description != "Lab LAN" {
		t.Errorf("Description = %q, want 'Lab LAN'", dto.Description)
	}
}

func TestSubnetService_CreateSubnet_InvalidCIDRs(t *testing.T) {
	svc := NewSubnetService(&mockSubnetRepo{})

	tests := []struct {
		name string
		cidr string
	}{
		{"non-canonical host bits", "192.168.10.5/24"},
		{"unsupported /31", "192.168.10.0/31"},
		{"unsupported /32", "192.168.10.1/32"},
		{"unsupported IPv6", "2001:db8::/32"},
		{"invalid format", "invalid"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.CreateSubnet(context.Background(), CreateSubnetRequest{
				CIDR: tt.cidr,
			})
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tt.cidr)
			}
			if !errors.Is(err, domain.ErrInvalidCIDR) {
				t.Errorf("expected ErrInvalidCIDR, got %v", err)
			}
		})
	}
}

func TestSubnetService_CreateSubnet_RepoOverlapError(t *testing.T) {
	repo := &mockSubnetRepo{
		createFn: func(ctx context.Context, subnet *domain.Subnet) error {
			return domain.ErrSubnetOverlap
		},
	}
	svc := NewSubnetService(repo)

	_, err := svc.CreateSubnet(context.Background(), CreateSubnetRequest{
		CIDR: "192.168.10.0/24",
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, domain.ErrSubnetOverlap) {
		t.Errorf("expected ErrSubnetOverlap, got %v", err)
	}
}
