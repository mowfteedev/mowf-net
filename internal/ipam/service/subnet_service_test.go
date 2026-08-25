package service

import (
	"context"
	"errors"
	"testing"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
)

type mockSubnetRepo struct {
	createFn  func(ctx context.Context, subnet *domain.Subnet) error
	getByIDFn func(ctx context.Context, id int64) (*repository.SubnetRead, error)
	listFn    func(ctx context.Context, filter repository.ListFilter) ([]*repository.SubnetRead, *int64, error)
	updateFn  func(ctx context.Context, id int64, patch repository.UpdateSubnet) (*repository.SubnetRead, error)
	deleteFn  func(ctx context.Context, id int64) error
}

func (m *mockSubnetRepo) Create(ctx context.Context, subnet *domain.Subnet) error {
	if m.createFn != nil {
		return m.createFn(ctx, subnet)
	}
	subnet.ID = 1
	return nil
}

func (m *mockSubnetRepo) GetByID(ctx context.Context, id int64) (*repository.SubnetRead, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, id)
	}
	return nil, domain.ErrSubnetNotFound
}

func (m *mockSubnetRepo) List(ctx context.Context, filter repository.ListFilter) ([]*repository.SubnetRead, *int64, error) {
	if m.listFn != nil {
		return m.listFn(ctx, filter)
	}
	return nil, nil, nil
}

func (m *mockSubnetRepo) Update(ctx context.Context, id int64, patch repository.UpdateSubnet) (*repository.SubnetRead, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, id, patch)
	}
	return nil, domain.ErrSubnetNotFound
}

func (m *mockSubnetRepo) Delete(ctx context.Context, id int64) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return domain.ErrSubnetNotFound
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

func TestSubnetService_GetSubnet(t *testing.T) {
	cidr, _ := domain.ParseCIDR("10.0.0.0/8")
	vlanRef := int64(7)
	mockSub := &repository.SubnetRead{Subnet: domain.Subnet{
		ID:          10,
		CIDR:        cidr,
		VlanRefID:   &vlanRef,
		Description: "Corporate WAN",
	}, AssignedCount: 2, ReservedCount: 3}

	repo := &mockSubnetRepo{
		getByIDFn: func(ctx context.Context, id int64) (*repository.SubnetRead, error) {
			if id == 10 {
				return mockSub, nil
			}
			return nil, domain.ErrSubnetNotFound
		},
	}
	svc := NewSubnetService(repo)

	t.Run("found", func(t *testing.T) {
		dto, err := svc.GetSubnet(context.Background(), 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dto.ID != 10 {
			t.Errorf("ID = %d, want 10", dto.ID)
		}
		if dto.CIDR != "10.0.0.0/8" {
			t.Errorf("CIDR = %s, want 10.0.0.0/8", dto.CIDR)
		}
		if dto.UsableCount != 16777214 {
			t.Errorf("UsableCount = %d, want 16777214", dto.UsableCount)
		}
		if dto.AssignedCount != 2 || dto.ReservedCount != 3 {
			t.Errorf("counts = assigned %d reserved %d, want 2 and 3", dto.AssignedCount, dto.ReservedCount)
		}
		if dto.AvailableCount != 16777209 {
			t.Errorf("AvailableCount = %d, want 16777209", dto.AvailableCount)
		}
		if dto.VlanRefID == nil || *dto.VlanRefID != 7 {
			t.Errorf("VlanRefID = %v, want 7", dto.VlanRefID)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := svc.GetSubnet(context.Background(), 999)
		if err == nil {
			t.Fatalf("expected error for non-existent ID, got nil")
		}
		if !errors.Is(err, domain.ErrSubnetNotFound) {
			t.Errorf("expected ErrSubnetNotFound, got %v", err)
		}
	})

	t.Run("invalid id <= 0", func(t *testing.T) {
		_, err := svc.GetSubnet(context.Background(), 0)
		if err == nil {
			t.Fatalf("expected error for ID 0, got nil")
		}
		if !errors.Is(err, domain.ErrSubnetNotFound) {
			t.Errorf("expected ErrSubnetNotFound for ID 0, got %v", err)
		}
	})
}

func TestSubnetService_ListSubnets(t *testing.T) {
	cidr1, _ := domain.ParseCIDR("192.168.1.0/24")
	cidr2, _ := domain.ParseCIDR("192.168.2.0/24")
	subnets := []*repository.SubnetRead{
		{Subnet: domain.Subnet{ID: 1, CIDR: cidr1, Description: "LAN 1"}, AssignedCount: 2, ReservedCount: 3},
		{Subnet: domain.Subnet{ID: 2, CIDR: cidr2, Description: "LAN 2"}, ReservedCount: 1},
	}

	nextCursorID := int64(2)
	repo := &mockSubnetRepo{
		listFn: func(ctx context.Context, filter repository.ListFilter) ([]*repository.SubnetRead, *int64, error) {
			return subnets, &nextCursorID, nil
		},
	}
	svc := NewSubnetService(repo)

	resp, err := svc.ListSubnets(context.Background(), ListSubnetsRequest{Limit: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Data) != 2 {
		t.Fatalf("len(resp.Data) = %d, want 2", len(resp.Data))
	}
	if resp.Page.Limit != 2 {
		t.Errorf("resp.Page.Limit = %d, want 2", resp.Page.Limit)
	}
	if resp.Page.NextCursor == nil {
		t.Fatalf("expected non-nil NextCursor")
	}
	if resp.Data[0].AssignedCount != 2 || resp.Data[0].ReservedCount != 3 || resp.Data[0].AvailableCount != 249 {
		t.Fatalf("mixed /24 DTO counts = assigned %d reserved %d available %d, want 2/3/249",
			resp.Data[0].AssignedCount, resp.Data[0].ReservedCount, resp.Data[0].AvailableCount)
	}

	decodedID, err := DecodeCursor(*resp.Page.NextCursor)
	if err != nil {
		t.Fatalf("failed to decode cursor %q: %v", *resp.Page.NextCursor, err)
	}
	if decodedID != 2 {
		t.Errorf("decoded cursor ID = %d, want 2", decodedID)
	}
}

func TestSubnetService_UpdateAndDelete(t *testing.T) {
	t.Run("valid update passes presence-aware patch", func(t *testing.T) {
		cidr, _ := domain.ParseCIDR("192.168.50.0/24")
		description := ""
		vlanID := int64(5)
		repo := &mockSubnetRepo{updateFn: func(ctx context.Context, id int64, patch repository.UpdateSubnet) (*repository.SubnetRead, error) {
			if id != 9 || !patch.CIDRSet || patch.CIDR != "192.168.50.0/24" || !patch.DescriptionSet || patch.Description != "" || !patch.VlanRefIDSet || patch.VlanRefID == nil || *patch.VlanRefID != 5 {
				t.Fatalf("unexpected repository update: id=%d patch=%+v", id, patch)
			}
			parsed, err := domain.ParseCIDR(patch.CIDR)
			if err != nil {
				t.Fatalf("mock received invalid CIDR: %v", err)
			}
			return &repository.SubnetRead{Subnet: domain.Subnet{ID: id, CIDR: parsed, Description: patch.Description, VlanRefID: patch.VlanRefID}}, nil
		}}
		svc := NewSubnetService(repo)
		cidrString := cidr.CIDR()
		if _, err := svc.UpdateSubnet(context.Background(), 9, UpdateSubnetRequest{
			CIDR: &cidrString, CIDRSet: true, Description: &description, DescriptionSet: true,
			VlanRefID: &vlanID, VlanRefIDSet: true,
		}); err != nil {
			t.Fatalf("valid UpdateSubnet failed: %v", err)
		}
	})

	invalid := []UpdateSubnetRequest{
		{},
		{CIDRSet: true},
		{DescriptionSet: true},
		{VlanRefIDSet: true, VlanRefID: func() *int64 { v := int64(0); return &v }()},
	}
	for i, req := range invalid {
		svc := NewSubnetService(&mockSubnetRepo{updateFn: func(context.Context, int64, repository.UpdateSubnet) (*repository.SubnetRead, error) {
			t.Fatal("repository called for invalid service update")
			return nil, nil
		}})
		if _, err := svc.UpdateSubnet(context.Background(), 1, req); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Errorf("invalid update %d error=%v, want ErrInvalidRequest", i, err)
		}
	}

	t.Run("delete delegates", func(t *testing.T) {
		called := false
		svc := NewSubnetService(&mockSubnetRepo{deleteFn: func(ctx context.Context, id int64) error {
			called = id == 8
			return nil
		}})
		if err := svc.DeleteSubnet(context.Background(), 8); err != nil || !called {
			t.Fatalf("DeleteSubnet error=%v called=%v", err, called)
		}
		if err := svc.DeleteSubnet(context.Background(), 0); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("invalid DeleteSubnet error=%v", err)
		}
	})
}

func TestCursorEncodingDecoding(t *testing.T) {
	tests := []int64{1, 42, 100, 999999999}
	for _, id := range tests {
		encoded := EncodeCursor(id)
		decoded, err := DecodeCursor(encoded)
		if err != nil {
			t.Fatalf("DecodeCursor(%q) error: %v", encoded, err)
		}
		if decoded != id {
			t.Errorf("decoded = %d, want %d", decoded, id)
		}
	}

	// Invalid cursors
	invalidCursors := []string{
		"not-base64-%%%",
		"YWJj", // base64 for "abc", not a number
		"MA==", // base64 for "0"
		"LTE=", // base64 for "-1"
	}
	for _, c := range invalidCursors {
		_, err := DecodeCursor(c)
		if err == nil {
			t.Errorf("DecodeCursor(%q) expected error, got nil", c)
		}
	}

	// Empty cursor should decode to 0 with no error
	id, err := DecodeCursor("")
	if err != nil || id != 0 {
		t.Errorf("DecodeCursor('') = (%d, %v), want (0, nil)", id, err)
	}
}
