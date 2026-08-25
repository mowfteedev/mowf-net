package postgres_test

import (
	"context"
	"net/netip"
	"reflect"
	"testing"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository/postgres"
	"github.com/mowfteedev/mowf-net/internal/ipam/service"
)

func addressStrings(addresses []netip.Addr) []string {
	result := make([]string, len(addresses))
	for i, address := range addresses {
		result[i] = address.String()
	}
	return result
}

func availableDTOAddresses(items []*service.AvailableIPDTO) []string {
	result := make([]string, len(items))
	for i, item := range items {
		result[i] = item.Address
	}
	return result
}

func TestAvailableIP_PostgresIntegration(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	subnet := createTestSubnet(t, subnetRepo, "10.70.0.0/24", nil, "available pool")
	interfaceID := int64(77)
	// Insert out of address order to prove PostgreSQL INET numeric ordering.
	insertAllocation(t, db, subnet.ID, "10.70.0.3", domain.AllocationStatusAssigned, &interfaceID, "assigned")
	insertAllocation(t, db, subnet.ID, "10.70.0.2", domain.AllocationStatusReserved, nil, "reserved")

	var rowCountBefore int
	if err := db.QueryRow("SELECT COUNT(*) FROM ip_allocations").Scan(&rowCountBefore); err != nil {
		t.Fatal(err)
	}
	occupied, err := allocationRepo.ListOccupiedAddresses(
		ctx,
		subnet.ID,
		netip.MustParseAddr("10.70.0.1"),
		netip.MustParseAddr("10.70.0.4"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := addressStrings(occupied), []string{"10.70.0.2", "10.70.0.3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("occupied range = %v, want %v", got, want)
	}

	availableService := service.NewAvailableIPService(subnetRepo, allocationRepo)
	for _, tc := range []struct {
		name string
		ip   string
		want []string
	}{
		{name: "available", ip: "10.70.0.1", want: []string{"10.70.0.1"}},
		{name: "reserved excluded", ip: "10.70.0.2", want: []string{}},
		{name: "assigned excluded", ip: "10.70.0.3", want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := availableService.ListAvailableIPs(ctx, service.ListAvailableIPsRequest{SubnetID: subnet.ID, Limit: 50, IP: tc.ip, IPSet: true})
			if err != nil {
				t.Fatal(err)
			}
			if got := availableDTOAddresses(response.Data); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("exact %s = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}

	firstPage, err := availableService.ListAvailableIPs(ctx, service.ListAvailableIPsRequest{SubnetID: subnet.ID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := availableDTOAddresses(firstPage.Data), []string{"10.70.0.1", "10.70.0.4"}; !reflect.DeepEqual(got, want) || firstPage.Page.NextCursor == nil {
		t.Fatalf("first range page = %v, cursor=%v", got, firstPage.Page.NextCursor)
	}
	secondPage, err := availableService.ListAvailableIPs(ctx, service.ListAvailableIPsRequest{
		SubnetID: subnet.ID, Limit: 2, Cursor: *firstPage.Page.NextCursor, CursorSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := availableDTOAddresses(secondPage.Data), []string{"10.70.0.5", "10.70.0.6"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second range page = %v, want %v", got, want)
	}

	// M1 count derivation remains backed by the same real persisted rows.
	subnetDTO, err := service.NewSubnetService(subnetRepo).GetSubnet(ctx, subnet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if subnetDTO.ReservedCount != 1 || subnetDTO.AssignedCount != 1 || subnetDTO.AvailableCount != 252 {
		t.Fatalf("M1 counts = reserved:%d assigned:%d available:%d", subnetDTO.ReservedCount, subnetDTO.AssignedCount, subnetDTO.AvailableCount)
	}

	// M2-A still reads both persisted states through its ID-pagination repository.
	allocations, _, err := allocationRepo.List(ctx, repository.AllocationListFilter{SubnetID: &subnet.ID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(allocations) != 2 || allocations[0].Status != domain.AllocationStatusAssigned || allocations[1].Status != domain.AllocationStatusReserved {
		t.Fatalf("M2-A persisted allocations = %#v", allocations)
	}

	var rowCountAfter int
	if err := db.QueryRow("SELECT COUNT(*) FROM ip_allocations").Scan(&rowCountAfter); err != nil {
		t.Fatal(err)
	}
	if rowCountAfter != rowCountBefore {
		t.Fatalf("available GET computation changed allocation rows: before=%d after=%d", rowCountBefore, rowCountAfter)
	}
}

func TestAvailableIP_PostgresSlash30Boundary(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	subnet := createTestSubnet(t, subnetRepo, "10.80.0.0/30", nil, "tiny")
	insertAllocation(t, db, subnet.ID, "10.80.0.1", domain.AllocationStatusReserved, nil, "occupied first usable")

	response, err := service.NewAvailableIPService(subnetRepo, allocationRepo).ListAvailableIPs(ctx, service.ListAvailableIPsRequest{
		SubnetID: subnet.ID, Limit: 50,
		RangeStart: "10.80.0.0", RangeStartSet: true,
		RangeEnd: "10.80.0.3", RangeEndSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := availableDTOAddresses(response.Data), []string{"10.80.0.2"}; !reflect.DeepEqual(got, want) || response.Page.NextCursor != nil {
		t.Fatalf("/30 available page = %v, cursor=%v", got, response.Page.NextCursor)
	}
}
