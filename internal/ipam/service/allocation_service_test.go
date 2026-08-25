package service

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
)

type mockAllocationRepo struct {
	listFn func(context.Context, repository.AllocationListFilter) ([]*domain.Allocation, *int64, error)
}

func (m *mockAllocationRepo) List(ctx context.Context, filter repository.AllocationListFilter) ([]*domain.Allocation, *int64, error) {
	if m.listFn != nil {
		return m.listFn(ctx, filter)
	}
	return nil, nil, nil
}

func TestAllocationService_ListAllocations(t *testing.T) {
	address := netip.MustParseAddr("192.168.10.20")
	interfaceID := int64(42)
	nextID := int64(8)
	var received repository.AllocationListFilter
	repo := &mockAllocationRepo{listFn: func(_ context.Context, filter repository.AllocationListFilter) ([]*domain.Allocation, *int64, error) {
		received = filter
		return []*domain.Allocation{
			{ID: 7, SubnetID: 10, Address: address, Status: domain.AllocationStatusReserved, Description: "Printer reservation"},
			{ID: 8, SubnetID: 10, Address: netip.MustParseAddr("192.168.10.21"), Status: domain.AllocationStatusAssigned, InterfaceID: &interfaceID},
		}, &nextID, nil
	}}

	status := domain.AllocationStatusReserved
	svc := NewAllocationService(repo)
	resp, err := svc.ListAllocations(context.Background(), ListAllocationsRequest{Status: &status, Limit: 1})
	if err != nil {
		t.Fatalf("ListAllocations() error = %v", err)
	}
	if received.Status == nil || *received.Status != domain.AllocationStatusReserved || received.Limit != 1 {
		t.Fatalf("unexpected repository filter: %#v", received)
	}
	if len(resp.Data) != 2 || resp.Data[0].Address != "192.168.10.20" || resp.Data[0].InterfaceID != nil {
		t.Fatalf("unexpected DTO data: %#v", resp.Data)
	}
	if resp.Data[1].Status != "assigned" || resp.Data[1].InterfaceID == nil || *resp.Data[1].InterfaceID != interfaceID {
		t.Fatalf("assigned DTO = %#v", resp.Data[1])
	}
	if resp.Page.Limit != 1 || resp.Page.NextCursor == nil {
		t.Fatalf("unexpected page: %#v", resp.Page)
	}
	if decoded, err := DecodeCursor(*resp.Page.NextCursor); err != nil || decoded != nextID {
		t.Fatalf("next cursor = (%d, %v), want (%d, nil)", decoded, err, nextID)
	}
}

func TestAllocationService_ListAllocations_DefaultAndInvalidLimits(t *testing.T) {
	var receivedLimit int
	svc := NewAllocationService(&mockAllocationRepo{listFn: func(_ context.Context, filter repository.AllocationListFilter) ([]*domain.Allocation, *int64, error) {
		receivedLimit = filter.Limit
		return nil, nil, nil
	}})
	if _, err := svc.ListAllocations(context.Background(), ListAllocationsRequest{}); err != nil {
		t.Fatalf("default limit returned error: %v", err)
	}
	if receivedLimit != 50 {
		t.Fatalf("default repository limit = %d, want 50", receivedLimit)
	}
	for _, limit := range []int{-1, 101} {
		if _, err := svc.ListAllocations(context.Background(), ListAllocationsRequest{Limit: limit}); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Errorf("limit %d error = %v, want ErrInvalidRequest", limit, err)
		}
	}
}
