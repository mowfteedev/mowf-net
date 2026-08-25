package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sort"
	"strings"
	"testing"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	ipamhttp "github.com/mowfteedev/mowf-net/internal/ipam/http"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository/postgres"
	"github.com/mowfteedev/mowf-net/internal/ipam/service"
)

func waitForLockingQuery(t *testing.T, db *sql.DB, description, queryFragment string) {
	t.Helper()
	waitForPostgresCondition(t, description, func() (bool, error) {
		var waiting bool
		err := db.QueryRow(`
			SELECT EXISTS(
				SELECT 1
				FROM pg_stat_activity
				WHERE datname = current_database()
				  AND wait_event_type = 'Lock'
				  AND position($1 in query) > 0
			)
		`, queryFragment).Scan(&waiting)
		return waiting, err
	})
}

func countAddressRows(t *testing.T, db *sql.DB, address string) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM ip_allocations WHERE address=$1::inet", address).Scan(&count); err != nil {
		t.Fatalf("failed to count allocation address %s: %v", address, err)
	}
	return count
}

func TestAllocationReservePostgresIntegration(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	allocationService := service.NewAllocationService(allocationRepo)
	availableService := service.NewAvailableIPService(subnetRepo, allocationRepo)
	subnetService := service.NewSubnetService(subnetRepo)
	subnet := createTestSubnet(t, subnetRepo, "10.120.0.0/24", nil, "reservation integration")
	target := netip.MustParseAddr("10.120.0.20")

	before, err := subnetService.GetSubnet(ctx, subnet.ID)
	if err != nil {
		t.Fatal(err)
	}
	availableBefore, err := availableService.ListAvailableIPs(ctx, service.ListAvailableIPsRequest{
		SubnetID: subnet.ID, Limit: 50, IP: target.String(), IPSet: true,
	})
	if err != nil || len(availableBefore.Data) != 1 || availableBefore.Data[0].Address != target.String() {
		t.Fatalf("available before = %#v, error = %v", availableBefore, err)
	}

	created, err := allocationService.ReserveAllocation(ctx, service.ReserveAllocationRequest{
		SubnetID: subnet.ID, Address: target, Description: " Printer reservation ",
	})
	if err != nil {
		t.Fatalf("ReserveAllocation() error = %v", err)
	}
	if created.ID <= 0 || created.SubnetID != subnet.ID || created.Address != target.String() || created.Status != "reserved" || created.InterfaceID != nil || created.Description != " Printer reservation " {
		t.Fatalf("created allocation = %#v", created)
	}

	var persistedStatus string
	var persistedInterface sql.NullInt64
	var persistedDescription string
	if err := db.QueryRow(`
		SELECT status, interface_id, description
		FROM ip_allocations
		WHERE id=$1 AND subnet_id=$2 AND address=$3::inet
	`, created.ID, subnet.ID, target.String()).Scan(&persistedStatus, &persistedInterface, &persistedDescription); err != nil {
		t.Fatalf("failed to read persisted reservation: %v", err)
	}
	if persistedStatus != "reserved" || persistedInterface.Valid || persistedDescription != " Printer reservation " {
		t.Fatalf("persisted state = status:%q interface:%v description:%q", persistedStatus, persistedInterface, persistedDescription)
	}

	reserved := domain.AllocationStatusReserved
	allocationRows, _, err := allocationRepo.List(ctx, repository.AllocationListFilter{
		SubnetID: &subnet.ID, Status: &reserved, Address: &target, Limit: 50,
	})
	if err != nil || len(allocationRows) != 1 || allocationRows[0].ID != created.ID {
		t.Fatalf("M2-A reservation list = %#v, error = %v", allocationRows, err)
	}
	after, err := subnetService.GetSubnet(ctx, subnet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ReservedCount != before.ReservedCount+1 || after.AssignedCount != before.AssignedCount || after.AvailableCount != before.AvailableCount-1 {
		t.Fatalf("M1 counts before=%+v after=%+v", before, after)
	}

	availableAfter, err := availableService.ListAvailableIPs(ctx, service.ListAvailableIPsRequest{
		SubnetID: subnet.ID, Limit: 50, IP: target.String(), IPSet: true,
	})
	if err != nil || len(availableAfter.Data) != 0 {
		t.Fatalf("M2-B exact after = %#v, error = %v", availableAfter, err)
	}
	rangeAfter, err := availableService.ListAvailableIPs(ctx, service.ListAvailableIPsRequest{
		SubnetID: subnet.ID, Limit: 50,
		RangeStart: "10.120.0.19", RangeStartSet: true,
		RangeEnd: "10.120.0.21", RangeEndSet: true,
	})
	if err != nil || len(rangeAfter.Data) != 2 || rangeAfter.Data[0].Address != "10.120.0.19" || rangeAfter.Data[1].Address != "10.120.0.21" {
		t.Fatalf("M2-B range after = %#v, error = %v", rangeAfter, err)
	}
	if got := countAddressRows(t, db, target.String()); got != 1 {
		t.Fatalf("final target row count = %d, want 1", got)
	}
}

func TestAllocationReservePostgresValidationAndBoundaries(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationService := service.NewAllocationService(postgres.NewAllocationRepository(db))
	subnet := createTestSubnet(t, subnetRepo, "10.121.0.0/24", nil, "validation")

	for _, tc := range []struct {
		name    string
		subnet  int64
		address string
		wantErr error
	}{
		{name: "missing subnet", subnet: subnet.ID + 999, address: "10.121.0.20", wantErr: domain.ErrSubnetNotFound},
		{name: "outside", subnet: subnet.ID, address: "10.122.0.20", wantErr: domain.ErrIPOutsideSubnet},
		{name: "network", subnet: subnet.ID, address: "10.121.0.0", wantErr: domain.ErrIPNotAssignable},
		{name: "broadcast", subnet: subnet.ID, address: "10.121.0.255", wantErr: domain.ErrIPNotAssignable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := allocationService.ReserveAllocation(ctx, service.ReserveAllocationRequest{
				SubnetID: tc.subnet, Address: netip.MustParseAddr(tc.address),
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
	for _, address := range []string{"10.121.0.1", "10.121.0.254"} {
		created, err := allocationService.ReserveAllocation(ctx, service.ReserveAllocationRequest{
			SubnetID: subnet.ID, Address: netip.MustParseAddr(address),
		})
		if err != nil || created.Address != address {
			t.Fatalf("boundary reservation %s = %#v, error = %v", address, created, err)
		}
	}
}

func TestAllocationReservePostgresDuplicateReservedAndAssigned(t *testing.T) {
	for _, status := range []domain.AllocationStatus{domain.AllocationStatusReserved, domain.AllocationStatusAssigned} {
		t.Run(string(status), func(t *testing.T) {
			db := setupTestDB(t)
			ctx := context.Background()
			subnetRepo := postgres.NewSubnetRepository(db)
			allocationService := service.NewAllocationService(postgres.NewAllocationRepository(db))
			subnet := createTestSubnet(t, subnetRepo, "10.122.0.0/24", nil, "duplicate")
			address := "10.122.0.20"
			if status == domain.AllocationStatusReserved {
				if _, err := allocationService.ReserveAllocation(ctx, service.ReserveAllocationRequest{
					SubnetID: subnet.ID, Address: netip.MustParseAddr(address),
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				interfaceID := int64(42)
				insertAllocation(t, db, subnet.ID, address, status, &interfaceID, "assigned collision")
			}

			_, err := allocationService.ReserveAllocation(ctx, service.ReserveAllocationRequest{
				SubnetID: subnet.ID, Address: netip.MustParseAddr(address),
			})
			if !errors.Is(err, domain.ErrIPAlreadyAllocated) {
				t.Fatalf("duplicate %s error = %v", status, err)
			}
			if got := countAddressRows(t, db, address); got != 1 {
				t.Fatalf("row count = %d, want 1", got)
			}
		})
	}
}

func TestAllocationReserveHTTPPersistsActualRow(t *testing.T) {
	db := setupTestDB(t)
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	subnet := createTestSubnet(t, subnetRepo, "10.123.0.0/24", nil, "HTTP")
	mux := http.NewServeMux()
	ipamhttp.NewAllocationHandler(service.NewAllocationService(allocationRepo)).RegisterRoutes(mux)

	body := fmt.Sprintf(`{"subnet_id":%d,"address":"10.123.0.20","description":"HTTP reservation"}`, subnet.ID)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ip-allocations", strings.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var response struct {
		Data service.AllocationDTO `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Data.ID <= 0 || response.Data.Status != "reserved" || response.Data.InterfaceID != nil || response.Data.Description != "HTTP reservation" {
		t.Fatalf("response = %#v", response.Data)
	}
	if got := countAddressRows(t, db, "10.123.0.20"); got != 1 {
		t.Fatalf("persisted rows = %d, want 1", got)
	}
}

func TestAllocationReserveConcurrentSameIPHTTP(t *testing.T) {
	db := setupTestDB(t)
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	subnet := createTestSubnet(t, subnetRepo, "10.124.0.0/24", nil, "same IP HTTP")
	mux := http.NewServeMux()
	ipamhttp.NewAllocationHandler(service.NewAllocationService(allocationRepo)).RegisterRoutes(mux)
	body := fmt.Sprintf(`{"subnet_id":%d,"address":"10.124.0.20"}`, subnet.ID)

	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			<-start
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/ip-allocations", strings.NewReader(body)))
			responses <- w
		}()
	}
	close(start)
	first, second := <-responses, <-responses
	statuses := []int{first.Code, second.Code}
	sort.Ints(statuses)
	if statuses[0] != http.StatusCreated || statuses[1] != http.StatusConflict {
		t.Fatalf("statuses = %v; bodies = %s / %s", statuses, first.Body.String(), second.Body.String())
	}
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code == http.StatusConflict {
			var failure ipamhttp.ErrorResponse
			if err := json.NewDecoder(response.Body).Decode(&failure); err != nil || failure.Error.Code != "IP_ALREADY_ALLOCATED" {
				t.Fatalf("conflict response = %#v, error = %v", failure, err)
			}
		}
	}
	if got := countAddressRows(t, db, "10.124.0.20"); got != 1 {
		t.Fatalf("final row count = %d, want 1", got)
	}
}

func TestAllocationReserveConcurrentUniqueConstraintContention(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	subnet := createTestSubnet(t, subnetRepo, "10.125.0.0/24", nil, "unique contention")
	address := netip.MustParseAddr("10.125.0.20")

	txs := make([]repository.AllocationReservationTransaction, 2)
	for i := range txs {
		tx, err := allocationRepo.BeginReservation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		txs[i] = tx
		defer func(tx repository.AllocationReservationTransaction) { _ = tx.Rollback() }(tx)
		cidr, err := tx.LockSubnet(ctx, subnet.ID)
		if err != nil || !cidr.IsUsable(address) {
			t.Fatalf("locked CIDR = %v, error = %v", cidr, err)
		}
	}

	type insertResult struct {
		index int
		err   error
	}
	start := make(chan struct{})
	results := make(chan insertResult, 2)
	for i, tx := range txs {
		go func(index int, reservationTx repository.AllocationReservationTransaction) {
			<-start
			err := reservationTx.InsertReserved(context.Background(), &domain.Allocation{
				SubnetID: subnet.ID, Address: address, Description: "unique race",
			})
			results <- insertResult{index: index, err: err}
		}(i, tx)
	}
	close(start)

	winner := <-results
	if winner.err != nil {
		t.Fatalf("first INSERT result = %v, want success", winner.err)
	}
	waitForLockingQuery(t, db, "same-IP loser waiting on UNIQUE(address)", "INSERT INTO ip_allocations")
	select {
	case loser := <-results:
		t.Fatalf("second INSERT completed before winner commit: index=%d error=%v", loser.index, loser.err)
	default:
	}
	if err := txs[winner.index].Commit(); err != nil {
		t.Fatalf("winner commit: %v", err)
	}
	loser := <-results
	if !errors.Is(loser.err, domain.ErrIPAlreadyAllocated) {
		t.Fatalf("loser error = %v, want ErrIPAlreadyAllocated", loser.err)
	}
	if err := txs[loser.index].Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("loser rollback: %v", err)
	}
	if got := countAddressRows(t, db, address.String()); got != 1 {
		t.Fatalf("final row count = %d, want 1", got)
	}
}

func TestAllocationReserveFirstBlocksResizeAndForcesRecheck(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	subnet := createTestSubnet(t, subnetRepo, "10.126.0.0/24", nil, "reserve first resize")
	address := netip.MustParseAddr("10.126.0.200")

	reservationTx, err := allocationRepo.BeginReservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reservationTx.Rollback() }()
	lockedCIDR, err := reservationTx.LockSubnet(ctx, subnet.ID)
	if err != nil || !lockedCIDR.IsUsable(address) {
		t.Fatalf("locked CIDR = %v, error = %v", lockedCIDR, err)
	}

	resizeDone := make(chan error, 1)
	go func() {
		_, err := subnetRepo.Update(context.Background(), subnet.ID, repository.UpdateSubnet{
			CIDR: "10.126.0.0/25", CIDRSet: true,
		})
		resizeDone <- err
	}()
	waitForLockingQuery(t, db, "Resize waiting behind Reserve FOR KEY SHARE", "FOR UPDATE")
	select {
	case err := <-resizeDone:
		t.Fatalf("Resize completed while Reserve held the Subnet lock: %v", err)
	default:
	}
	allocation := &domain.Allocation{SubnetID: subnet.ID, Address: address, Description: "committed before resize"}
	if err := reservationTx.InsertReserved(ctx, allocation); err != nil {
		t.Fatal(err)
	}
	if err := reservationTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-resizeDone; !errors.Is(err, domain.ErrSubnetResizeConflict) {
		t.Fatalf("Resize error = %v, want ErrSubnetResizeConflict", err)
	}
	read, err := subnetRepo.GetByID(ctx, subnet.ID)
	if err != nil || read.CIDR.CIDR() != "10.126.0.0/24" || !read.CIDR.IsUsable(address) {
		t.Fatalf("final subnet = %+v, error = %v", read, err)
	}
}

func TestAllocationResizeFirstBlocksReserveAndUsesNewCIDR(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationService := service.NewAllocationService(postgres.NewAllocationRepository(db))
	subnet := createTestSubnet(t, subnetRepo, "10.127.0.0/24", nil, "resize first")

	const pauseKey int64 = 0x4D3243524553495A
	if _, err := db.Exec(fmt.Sprintf(`
		CREATE FUNCTION test_pause_m2c_subnet_resize() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM pg_advisory_xact_lock(%d);
			RETURN NEW;
		END $$;
		CREATE TRIGGER test_pause_m2c_subnet_resize_trigger
		BEFORE UPDATE ON subnets FOR EACH ROW EXECUTE FUNCTION test_pause_m2c_subnet_resize();
	`, pauseKey)); err != nil {
		t.Fatalf("failed to install Resize pause trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`
			DROP TRIGGER IF EXISTS test_pause_m2c_subnet_resize_trigger ON subnets;
			DROP FUNCTION IF EXISTS test_pause_m2c_subnet_resize();
		`)
	})
	pauseHolder, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pauseHolder.Rollback() }()
	if _, err := pauseHolder.Exec("SELECT pg_advisory_xact_lock($1)", pauseKey); err != nil {
		t.Fatal(err)
	}

	resizeDone := make(chan error, 1)
	go func() {
		_, err := subnetRepo.Update(context.Background(), subnet.ID, repository.UpdateSubnet{
			CIDR: "10.127.0.0/25", CIDRSet: true,
		})
		resizeDone <- err
	}()
	waitForPostgresCondition(t, "production Resize paused after FOR UPDATE", func() (bool, error) {
		count, err := advisoryLockCount(db, false)
		return count > 0, err
	})

	reserveDone := make(chan error, 1)
	go func() {
		_, err := allocationService.ReserveAllocation(context.Background(), service.ReserveAllocationRequest{
			SubnetID: subnet.ID, Address: netip.MustParseAddr("10.127.0.200"),
		})
		reserveDone <- err
	}()
	waitForLockingQuery(t, db, "Reserve FOR KEY SHARE waiting behind Resize", "FOR KEY SHARE")
	select {
	case err := <-reserveDone:
		t.Fatalf("Reserve completed before Resize commit: %v", err)
	default:
	}
	if err := pauseHolder.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-resizeDone; err != nil {
		t.Fatalf("Resize failed: %v", err)
	}
	if err := <-reserveDone; !errors.Is(err, domain.ErrIPOutsideSubnet) {
		t.Fatalf("Reserve error = %v, want ErrIPOutsideSubnet against new CIDR", err)
	}
	read, err := subnetRepo.GetByID(ctx, subnet.ID)
	if err != nil || read.CIDR.CIDR() != "10.127.0.0/25" {
		t.Fatalf("final subnet = %+v, error = %v", read, err)
	}
	if got := countAddressRows(t, db, "10.127.0.200"); got != 0 {
		t.Fatalf("stale-CIDR reservation count = %d, want 0", got)
	}
}

func TestAllocationReserveFirstBlocksDeleteAndPreservesSubnet(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	subnet := createTestSubnet(t, subnetRepo, "10.128.0.0/24", nil, "reserve first delete")
	address := netip.MustParseAddr("10.128.0.20")

	reservationTx, err := allocationRepo.BeginReservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reservationTx.Rollback() }()
	lockedCIDR, err := reservationTx.LockSubnet(ctx, subnet.ID)
	if err != nil || !lockedCIDR.IsUsable(address) {
		t.Fatalf("locked CIDR = %v, error = %v", lockedCIDR, err)
	}

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- subnetRepo.Delete(context.Background(), subnet.ID) }()
	waitForLockingQuery(t, db, "Delete waiting behind Reserve FOR KEY SHARE", "FOR UPDATE")
	select {
	case err := <-deleteDone:
		t.Fatalf("Delete completed while Reserve held the Subnet lock: %v", err)
	default:
	}
	if err := reservationTx.InsertReserved(ctx, &domain.Allocation{SubnetID: subnet.ID, Address: address}); err != nil {
		t.Fatal(err)
	}
	if err := reservationTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-deleteDone; !errors.Is(err, domain.ErrSubnetHasAllocations) {
		t.Fatalf("Delete error = %v, want ErrSubnetHasAllocations", err)
	}
	if _, err := subnetRepo.GetByID(ctx, subnet.ID); err != nil {
		t.Fatalf("Subnet did not remain: %v", err)
	}
	if got := countAddressRows(t, db, address.String()); got != 1 {
		t.Fatalf("reservation count = %d, want 1", got)
	}
}

func TestAllocationDeleteFirstBlocksReserveAndPreventsOrphan(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationService := service.NewAllocationService(postgres.NewAllocationRepository(db))
	subnet := createTestSubnet(t, subnetRepo, "10.129.0.0/24", nil, "delete first")

	const pauseKey int64 = 0x4D324344454C4554
	if _, err := db.Exec(fmt.Sprintf(`
		CREATE FUNCTION test_pause_m2c_subnet_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM pg_advisory_xact_lock(%d);
			RETURN OLD;
		END $$;
		CREATE TRIGGER test_pause_m2c_subnet_delete_trigger
		BEFORE DELETE ON subnets FOR EACH ROW EXECUTE FUNCTION test_pause_m2c_subnet_delete();
	`, pauseKey)); err != nil {
		t.Fatalf("failed to install Delete pause trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`
			DROP TRIGGER IF EXISTS test_pause_m2c_subnet_delete_trigger ON subnets;
			DROP FUNCTION IF EXISTS test_pause_m2c_subnet_delete();
		`)
	})
	pauseHolder, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pauseHolder.Rollback() }()
	if _, err := pauseHolder.Exec("SELECT pg_advisory_xact_lock($1)", pauseKey); err != nil {
		t.Fatal(err)
	}

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- subnetRepo.Delete(context.Background(), subnet.ID) }()
	waitForPostgresCondition(t, "production Delete paused after FOR UPDATE", func() (bool, error) {
		count, err := advisoryLockCount(db, false)
		return count > 0, err
	})

	reserveDone := make(chan error, 1)
	go func() {
		_, err := allocationService.ReserveAllocation(context.Background(), service.ReserveAllocationRequest{
			SubnetID: subnet.ID, Address: netip.MustParseAddr("10.129.0.20"),
		})
		reserveDone <- err
	}()
	waitForLockingQuery(t, db, "Reserve FOR KEY SHARE waiting behind Delete", "FOR KEY SHARE")
	select {
	case err := <-reserveDone:
		t.Fatalf("Reserve completed before Delete commit: %v", err)
	default:
	}
	if err := pauseHolder.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if err := <-reserveDone; !errors.Is(err, domain.ErrSubnetNotFound) {
		t.Fatalf("Reserve error = %v, want ErrSubnetNotFound", err)
	}
	if got := countAddressRows(t, db, "10.129.0.20"); got != 0 {
		t.Fatalf("orphan reservation count = %d, want 0", got)
	}
}

func TestAllocationReserveDoesNotUseSubnetAdvisoryLock(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationService := service.NewAllocationService(postgres.NewAllocationRepository(db))
	subnet := createTestSubnet(t, subnetRepo, "10.130.0.0/24", nil, "no advisory")

	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.Exec("SELECT pg_advisory_xact_lock($1)", postgres.SubnetCoordinationKey); err != nil {
		t.Fatal(err)
	}
	if _, err := allocationService.ReserveAllocation(ctx, service.ReserveAllocationRequest{
		SubnetID: subnet.ID, Address: netip.MustParseAddr("10.130.0.20"),
	}); err != nil {
		t.Fatalf("Reserve waited on or failed because of the Subnet advisory lock: %v", err)
	}
}
