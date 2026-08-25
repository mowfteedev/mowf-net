package service

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
)

func mustAllocationTestCIDR(t *testing.T, raw string) domain.CIDR {
	t.Helper()
	cidr, err := domain.ParseCIDR(raw)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", raw, err)
	}
	return cidr
}

type mockAllocationRepo struct {
	listFn           func(context.Context, repository.AllocationListFilter) ([]*domain.Allocation, *int64, error)
	beginFn          func(context.Context) (repository.AllocationReservationTransaction, error)
	beginUnreserveFn func(context.Context) (repository.AllocationUnreservationTransaction, error)
}

func (m *mockAllocationRepo) List(ctx context.Context, filter repository.AllocationListFilter) ([]*domain.Allocation, *int64, error) {
	if m.listFn != nil {
		return m.listFn(ctx, filter)
	}
	return nil, nil, nil
}

func (m *mockAllocationRepo) BeginReservation(ctx context.Context) (repository.AllocationReservationTransaction, error) {
	if m.beginFn != nil {
		return m.beginFn(ctx)
	}
	return nil, errors.New("unexpected BeginReservation call")
}

func (m *mockAllocationRepo) BeginUnreservation(ctx context.Context) (repository.AllocationUnreservationTransaction, error) {
	if m.beginUnreserveFn != nil {
		return m.beginUnreserveFn(ctx)
	}
	return nil, errors.New("unexpected BeginUnreservation call")
}

type mockReservationTransaction struct {
	lockFn     func(context.Context, int64) (domain.CIDR, error)
	insertFn   func(context.Context, *domain.Allocation) error
	commitFn   func() error
	rollbackFn func() error
}

func (m *mockReservationTransaction) LockSubnet(ctx context.Context, subnetID int64) (domain.CIDR, error) {
	return m.lockFn(ctx, subnetID)
}

func (m *mockReservationTransaction) InsertReserved(ctx context.Context, allocation *domain.Allocation) error {
	return m.insertFn(ctx, allocation)
}

func (m *mockReservationTransaction) Commit() error {
	return m.commitFn()
}

func (m *mockReservationTransaction) Rollback() error {
	return m.rollbackFn()
}

type mockUnreservationTransaction struct {
	lockFn     func(context.Context, int64) (*domain.Allocation, error)
	deleteFn   func(context.Context, int64) error
	commitFn   func() error
	rollbackFn func() error
}

func (m *mockUnreservationTransaction) LockAllocation(ctx context.Context, allocationID int64) (*domain.Allocation, error) {
	return m.lockFn(ctx, allocationID)
}

func (m *mockUnreservationTransaction) DeleteLockedAllocation(ctx context.Context, allocationID int64) error {
	return m.deleteFn(ctx, allocationID)
}

func (m *mockUnreservationTransaction) Commit() error {
	return m.commitFn()
}

func (m *mockUnreservationTransaction) Rollback() error {
	return m.rollbackFn()
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

func TestAllocationService_ReserveAllocationUsesLockedCIDRAndCommitsInsertedRow(t *testing.T) {
	lockedCIDR := mustAllocationTestCIDR(t, "192.168.10.0/24")
	address := netip.MustParseAddr("192.168.10.20")
	var order []string
	tx := &mockReservationTransaction{
		lockFn: func(_ context.Context, subnetID int64) (domain.CIDR, error) {
			order = append(order, "lock")
			if subnetID != 10 {
				t.Fatalf("locked subnet ID = %d, want 10", subnetID)
			}
			return lockedCIDR, nil
		},
		insertFn: func(_ context.Context, allocation *domain.Allocation) error {
			order = append(order, "insert")
			if allocation.SubnetID != 10 || allocation.Address != address || allocation.Status != domain.AllocationStatusReserved || allocation.InterfaceID != nil || allocation.Description != "Printer reservation" {
				t.Fatalf("unexpected allocation before insert: %#v", allocation)
			}
			allocation.ID = 100
			return nil
		},
		commitFn: func() error {
			order = append(order, "commit")
			return nil
		},
		rollbackFn: func() error {
			order = append(order, "rollback")
			return nil
		},
	}
	repo := &mockAllocationRepo{beginFn: func(context.Context) (repository.AllocationReservationTransaction, error) {
		order = append(order, "begin")
		return tx, nil
	}}

	dto, err := NewAllocationService(repo).ReserveAllocation(context.Background(), ReserveAllocationRequest{
		SubnetID: 10, Address: address, Description: "Printer reservation",
	})
	if err != nil {
		t.Fatalf("ReserveAllocation() error = %v", err)
	}
	if got, want := strings.Join(order, ","), "begin,lock,insert,commit"; got != want {
		t.Fatalf("operation order = %q, want %q", got, want)
	}
	if dto.ID != 100 || dto.Status != "reserved" || dto.InterfaceID != nil || dto.Description != "Printer reservation" {
		t.Fatalf("unexpected DTO: %#v", dto)
	}
}

func TestAllocationService_ReserveAllocationValidatesAgainstLockedCIDRAndRollsBack(t *testing.T) {
	lockedCIDR := mustAllocationTestCIDR(t, "192.168.20.0/24")
	for _, tc := range []struct {
		name    string
		address string
		wantErr error
	}{
		{name: "outside", address: "192.168.10.20", wantErr: domain.ErrIPOutsideSubnet},
		{name: "network", address: "192.168.20.0", wantErr: domain.ErrIPNotAssignable},
		{name: "broadcast", address: "192.168.20.255", wantErr: domain.ErrIPNotAssignable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inserted := false
			rolledBack := false
			tx := &mockReservationTransaction{
				lockFn: func(context.Context, int64) (domain.CIDR, error) { return lockedCIDR, nil },
				insertFn: func(context.Context, *domain.Allocation) error {
					inserted = true
					return nil
				},
				commitFn: func() error { return nil },
				rollbackFn: func() error {
					rolledBack = true
					return nil
				},
			}
			repo := &mockAllocationRepo{beginFn: func(context.Context) (repository.AllocationReservationTransaction, error) { return tx, nil }}
			_, err := NewAllocationService(repo).ReserveAllocation(context.Background(), ReserveAllocationRequest{
				SubnetID: 10, Address: netip.MustParseAddr(tc.address),
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if inserted || !rolledBack {
				t.Fatalf("inserted=%v rolledBack=%v", inserted, rolledBack)
			}
		})
	}
}

func TestAllocationService_UnreserveAllocationLocksDeletesAndCommits(t *testing.T) {
	var order []string
	tx := &mockUnreservationTransaction{
		lockFn: func(_ context.Context, allocationID int64) (*domain.Allocation, error) {
			order = append(order, "lock")
			return &domain.Allocation{ID: allocationID, Status: domain.AllocationStatusReserved}, nil
		},
		deleteFn: func(_ context.Context, allocationID int64) error {
			order = append(order, "delete")
			if allocationID != 100 {
				t.Fatalf("deleted allocation ID = %d, want 100", allocationID)
			}
			return nil
		},
		commitFn: func() error {
			order = append(order, "commit")
			return nil
		},
		rollbackFn: func() error {
			order = append(order, "rollback")
			return nil
		},
	}
	repo := &mockAllocationRepo{beginUnreserveFn: func(context.Context) (repository.AllocationUnreservationTransaction, error) {
		order = append(order, "begin")
		return tx, nil
	}}
	if err := NewAllocationService(repo).UnreserveAllocation(context.Background(), 100); err != nil {
		t.Fatalf("UnreserveAllocation() error = %v", err)
	}
	if got, want := strings.Join(order, ","), "begin,lock,delete,commit"; got != want {
		t.Fatalf("operation order = %q, want %q", got, want)
	}
}

func TestAllocationService_UnreserveAllocationRejectsCurrentStateAndRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name       string
		allocation *domain.Allocation
		lockErr    error
		wantErr    error
	}{
		{name: "missing", lockErr: domain.ErrIPAllocationNotFound, wantErr: domain.ErrIPAllocationNotFound},
		{name: "assigned", allocation: &domain.Allocation{ID: 100, Status: domain.AllocationStatusAssigned}, wantErr: domain.ErrIPNotAssignable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deleted := false
			rolledBack := false
			tx := &mockUnreservationTransaction{
				lockFn: func(context.Context, int64) (*domain.Allocation, error) { return tc.allocation, tc.lockErr },
				deleteFn: func(context.Context, int64) error {
					deleted = true
					return nil
				},
				commitFn: func() error { return nil },
				rollbackFn: func() error {
					rolledBack = true
					return nil
				},
			}
			repo := &mockAllocationRepo{beginUnreserveFn: func(context.Context) (repository.AllocationUnreservationTransaction, error) {
				return tx, nil
			}}
			err := NewAllocationService(repo).UnreserveAllocation(context.Background(), 100)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if deleted || !rolledBack {
				t.Fatalf("deleted=%v rolledBack=%v", deleted, rolledBack)
			}
		})
	}
}

func TestAllocationService_UnreserveAllocationInvalidIDDoesNotBegin(t *testing.T) {
	beginCalls := 0
	repo := &mockAllocationRepo{beginUnreserveFn: func(context.Context) (repository.AllocationUnreservationTransaction, error) {
		beginCalls++
		return nil, errors.New("must not begin")
	}}
	for _, allocationID := range []int64{0, -1} {
		if err := NewAllocationService(repo).UnreserveAllocation(context.Background(), allocationID); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("ID %d error = %v, want ErrInvalidRequest", allocationID, err)
		}
	}
	if beginCalls != 0 {
		t.Fatalf("BeginUnreservation calls = %d, want 0", beginCalls)
	}
}

func TestAllocationService_UnreserveAllocationFailuresRollBack(t *testing.T) {
	for _, tc := range []struct {
		name      string
		deleteErr error
		commitErr error
		wantOrder string
	}{
		{name: "delete", deleteErr: errors.New("delete failure"), wantOrder: "begin,lock,delete,rollback"},
		{name: "commit", commitErr: errors.New("commit failure"), wantOrder: "begin,lock,delete,commit,rollback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			tx := &mockUnreservationTransaction{
				lockFn: func(_ context.Context, allocationID int64) (*domain.Allocation, error) {
					order = append(order, "lock")
					return &domain.Allocation{ID: allocationID, Status: domain.AllocationStatusReserved}, nil
				},
				deleteFn: func(context.Context, int64) error {
					order = append(order, "delete")
					return tc.deleteErr
				},
				commitFn: func() error {
					order = append(order, "commit")
					return tc.commitErr
				},
				rollbackFn: func() error {
					order = append(order, "rollback")
					return nil
				},
			}
			repo := &mockAllocationRepo{beginUnreserveFn: func(context.Context) (repository.AllocationUnreservationTransaction, error) {
				order = append(order, "begin")
				return tx, nil
			}}
			if err := NewAllocationService(repo).UnreserveAllocation(context.Background(), 100); err == nil {
				t.Fatal("UnreserveAllocation() error = nil")
			}
			if got := strings.Join(order, ","); got != tc.wantOrder {
				t.Fatalf("operation order = %q, want %q", got, tc.wantOrder)
			}
		})
	}
}
