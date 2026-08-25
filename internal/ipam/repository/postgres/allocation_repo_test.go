package postgres_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository/postgres"
)

func insertAllocation(t *testing.T, dbQuery interface {
	QueryRow(string, ...any) *sql.Row
}, subnetID int64, address string, status domain.AllocationStatus, interfaceID *int64, description string) int64 {
	t.Helper()
	var id int64
	if err := dbQuery.QueryRow(`
		INSERT INTO ip_allocations (subnet_id, address, status, interface_id, description)
		VALUES ($1, $2::inet, $3, $4, $5)
		RETURNING id
	`, subnetID, address, string(status), interfaceID, description).Scan(&id); err != nil {
		t.Fatalf("insert allocation: %v", err)
	}
	return id
}

func TestAllocationRepo_List_FiltersAndPagination(t *testing.T) {
	db := setupTestDB(t)
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	firstSubnet := createTestSubnet(t, subnetRepo, "10.0.0.0/24", nil, "first")
	secondSubnet := createTestSubnet(t, subnetRepo, "10.0.1.0/24", nil, "second")
	interfaceSeven, interfaceEight := int64(7), int64(8)
	firstID := insertAllocation(t, db, firstSubnet.ID, "10.0.0.10", domain.AllocationStatusReserved, nil, "reserved")
	secondID := insertAllocation(t, db, firstSubnet.ID, "10.0.0.11", domain.AllocationStatusAssigned, &interfaceSeven, "assigned")
	thirdID := insertAllocation(t, db, secondSubnet.ID, "10.0.1.10", domain.AllocationStatusReserved, nil, "other reserved")
	_ = insertAllocation(t, db, secondSubnet.ID, "10.0.1.11", domain.AllocationStatusAssigned, &interfaceEight, "other assigned")

	ctx := context.Background()
	page, cursor, err := allocationRepo.List(ctx, repository.AllocationListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List first page: %v", err)
	}
	if len(page) != 2 || page[0].ID != firstID || page[1].ID != secondID || cursor == nil || *cursor != secondID {
		t.Fatalf("first page/cursor = %#v/%v", page, cursor)
	}
	page, cursor, err = allocationRepo.List(ctx, repository.AllocationListFilter{Limit: 2, Cursor: cursor})
	if err != nil {
		t.Fatalf("List continuation: %v", err)
	}
	if len(page) != 2 || page[0].ID != thirdID || cursor != nil {
		t.Fatalf("continuation page/cursor = %#v/%v", page, cursor)
	}

	reserved := domain.AllocationStatusReserved
	page, _, err = allocationRepo.List(ctx, repository.AllocationListFilter{Status: &reserved, Limit: 50})
	if err != nil || len(page) != 2 || page[0].Status != reserved || page[1].Status != reserved {
		t.Fatalf("reserved filter = %#v, %v", page, err)
	}
	assigned := domain.AllocationStatusAssigned
	page, _, err = allocationRepo.List(ctx, repository.AllocationListFilter{Status: &assigned, Limit: 50})
	if err != nil || len(page) != 2 || page[0].Status != assigned || page[1].Status != assigned {
		t.Fatalf("assigned filter = %#v, %v", page, err)
	}
	page, _, err = allocationRepo.List(ctx, repository.AllocationListFilter{SubnetID: &secondSubnet.ID, Limit: 50})
	if err != nil || len(page) != 2 || page[0].SubnetID != secondSubnet.ID || page[1].SubnetID != secondSubnet.ID {
		t.Fatalf("subnet filter = %#v, %v", page, err)
	}
	page, _, err = allocationRepo.List(ctx, repository.AllocationListFilter{InterfaceID: &interfaceEight, Limit: 50})
	if err != nil || len(page) != 1 || page[0].InterfaceID == nil || *page[0].InterfaceID != interfaceEight {
		t.Fatalf("interface filter = %#v, %v", page, err)
	}
	page, _, err = allocationRepo.List(ctx, repository.AllocationListFilter{SubnetID: &firstSubnet.ID, InterfaceID: &interfaceSeven, Limit: 50})
	if err != nil || len(page) != 1 || page[0].ID != secondID || page[0].Address.String() != "10.0.0.11" {
		t.Fatalf("combined filter = %#v, %v", page, err)
	}
	address := page[0].Address
	page, _, err = allocationRepo.List(ctx, repository.AllocationListFilter{Address: &address, Limit: 50})
	if err != nil || len(page) != 1 || page[0].ID != secondID {
		t.Fatalf("address filter = %#v, %v", page, err)
	}
}

func TestAllocationRepo_List_Empty(t *testing.T) {
	db := setupTestDB(t)
	allocations, cursor, err := postgres.NewAllocationRepository(db).List(context.Background(), repository.AllocationListFilter{Limit: 50})
	if err != nil || len(allocations) != 0 || cursor != nil {
		t.Fatalf("empty list = %#v, %v, %v", allocations, cursor, err)
	}
}
