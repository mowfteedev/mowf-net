package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	ipamhttp "github.com/mowfteedev/mowf-net/internal/ipam/http"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository/postgres"
	"github.com/mowfteedev/mowf-net/internal/ipam/service"
)

func setupUnreservationTestMux(subnetRepo *postgres.SubnetRepository, allocationRepo *postgres.AllocationRepository) *http.ServeMux {
	mux := http.NewServeMux()
	ipamhttp.NewAllocationHandler(service.NewAllocationService(allocationRepo)).RegisterRoutes(mux)
	ipamhttp.NewAvailableIPHandler(service.NewAvailableIPService(subnetRepo, allocationRepo)).RegisterRoutes(mux)
	return mux
}

func serveUnreservationRequest(mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, target, strings.NewReader(body)))
	return w
}

func decodeAvailableIPResponse(t *testing.T, response *httptest.ResponseRecorder) service.ListAvailableIPsResponse {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("available-IP status = %d, body = %s", response.Code, response.Body.String())
	}
	var decoded service.ListAvailableIPsResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode available-IP response: %v", err)
	}
	return decoded
}

func assertSubnetIdentityUnchanged(t *testing.T, before, after *service.SubnetDTO) {
	t.Helper()
	if after.ID != before.ID || after.CIDR != before.CIDR || after.Network != before.Network ||
		after.Broadcast != before.Broadcast || after.FirstUsable != before.FirstUsable ||
		after.LastUsable != before.LastUsable || after.UsableCount != before.UsableCount ||
		after.Description != before.Description {
		t.Fatalf("Subnet identity changed: before=%+v after=%+v", before, after)
	}
	if (before.VlanRefID == nil) != (after.VlanRefID == nil) ||
		before.VlanRefID != nil && *before.VlanRefID != *after.VlanRefID {
		t.Fatalf("Subnet VLAN changed: before=%v after=%v", before.VlanRefID, after.VlanRefID)
	}
}

func TestAllocationUnreserveRoundTripPostgresIntegration(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	subnetService := service.NewSubnetService(subnetRepo)
	mux := setupUnreservationTestMux(subnetRepo, allocationRepo)
	subnet := createTestSubnet(t, subnetRepo, "10.131.0.0/24", nil, "M2-D round trip")
	target := "10.131.0.20"

	before, err := subnetService.GetSubnet(ctx, subnet.ID)
	if err != nil {
		t.Fatal(err)
	}
	exactURL := fmt.Sprintf("/api/v1/subnets/%d/available-ips?ip=%s", subnet.ID, target)
	rangeURL := fmt.Sprintf("/api/v1/subnets/%d/available-ips?range_start=10.131.0.19&range_end=10.131.0.21", subnet.ID)
	exactBefore := decodeAvailableIPResponse(t, serveUnreservationRequest(mux, http.MethodGet, exactURL, ""))
	if len(exactBefore.Data) != 1 || exactBefore.Data[0].Address != target || exactBefore.Data[0].Persisted || exactBefore.Data[0].State != "available" {
		t.Fatalf("exact availability before Reserve = %#v", exactBefore.Data)
	}

	reserveBody := fmt.Sprintf(`{"subnet_id":%d,"address":%q,"description":"round trip reservation"}`, subnet.ID, target)
	reserveResponse := serveUnreservationRequest(mux, http.MethodPost, "/api/v1/ip-allocations", reserveBody)
	if reserveResponse.Code != http.StatusCreated {
		t.Fatalf("Reserve status = %d, body = %s", reserveResponse.Code, reserveResponse.Body.String())
	}
	var reserved struct {
		Data service.AllocationDTO `json:"data"`
	}
	if err := json.NewDecoder(reserveResponse.Body).Decode(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved.Data.ID <= 0 || reserved.Data.Status != "reserved" || reserved.Data.InterfaceID != nil {
		t.Fatalf("reserved response = %#v", reserved.Data)
	}
	if got := countAddressRows(t, db, target); got != 1 {
		t.Fatalf("persisted reservation count = %d, want 1", got)
	}

	afterReserve, err := subnetService.GetSubnet(ctx, subnet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterReserve.ReservedCount != before.ReservedCount+1 || afterReserve.AssignedCount != before.AssignedCount || afterReserve.AvailableCount != before.AvailableCount-1 {
		t.Fatalf("counts before=%+v after Reserve=%+v", before, afterReserve)
	}
	listURL := fmt.Sprintf("/api/v1/ip-allocations?subnet_id=%d&status=reserved&address=%s", subnet.ID, url.QueryEscape(target))
	listBeforeDelete := serveUnreservationRequest(mux, http.MethodGet, listURL, "")
	var listed service.ListAllocationsResponse
	if listBeforeDelete.Code != http.StatusOK || json.NewDecoder(listBeforeDelete.Body).Decode(&listed) != nil || len(listed.Data) != 1 || listed.Data[0].ID != reserved.Data.ID {
		t.Fatalf("M2-A before DELETE status/body/data = %d/%s/%#v", listBeforeDelete.Code, listBeforeDelete.Body.String(), listed.Data)
	}
	exactWhileReserved := decodeAvailableIPResponse(t, serveUnreservationRequest(mux, http.MethodGet, exactURL, ""))
	if len(exactWhileReserved.Data) != 0 {
		t.Fatalf("exact availability while reserved = %#v", exactWhileReserved.Data)
	}
	rangeWhileReserved := decodeAvailableIPResponse(t, serveUnreservationRequest(mux, http.MethodGet, rangeURL, ""))
	if len(rangeWhileReserved.Data) != 2 || rangeWhileReserved.Data[0].Address != "10.131.0.19" || rangeWhileReserved.Data[1].Address != "10.131.0.21" {
		t.Fatalf("range availability while reserved = %#v", rangeWhileReserved.Data)
	}

	deleteResponse := serveUnreservationRequest(mux, http.MethodDelete, fmt.Sprintf("/api/v1/ip-allocations/%d", reserved.Data.ID), "")
	if deleteResponse.Code != http.StatusNoContent || deleteResponse.Body.Len() != 0 {
		t.Fatalf("DELETE status/body length/body = %d/%d/%q", deleteResponse.Code, deleteResponse.Body.Len(), deleteResponse.Body.String())
	}
	if got := countAddressRows(t, db, target); got != 0 {
		t.Fatalf("allocation row count after Unreserve = %d, want 0", got)
	}

	listAfterDelete := serveUnreservationRequest(mux, http.MethodGet, listURL, "")
	listed = service.ListAllocationsResponse{}
	if listAfterDelete.Code != http.StatusOK || json.NewDecoder(listAfterDelete.Body).Decode(&listed) != nil || len(listed.Data) != 0 {
		t.Fatalf("M2-A after DELETE status/body/data = %d/%s/%#v", listAfterDelete.Code, listAfterDelete.Body.String(), listed.Data)
	}
	afterUnreserve, err := subnetService.GetSubnet(ctx, subnet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterUnreserve.ReservedCount != before.ReservedCount || afterUnreserve.AssignedCount != before.AssignedCount || afterUnreserve.AvailableCount != before.AvailableCount {
		t.Fatalf("counts before=%+v after Unreserve=%+v", before, afterUnreserve)
	}
	assertSubnetIdentityUnchanged(t, before, afterUnreserve)

	exactAfter := decodeAvailableIPResponse(t, serveUnreservationRequest(mux, http.MethodGet, exactURL, ""))
	if len(exactAfter.Data) != 1 || exactAfter.Data[0].Address != target || exactAfter.Data[0].Persisted || exactAfter.Data[0].State != "available" {
		t.Fatalf("exact availability after Unreserve = %#v", exactAfter.Data)
	}
	rangeAfter := decodeAvailableIPResponse(t, serveUnreservationRequest(mux, http.MethodGet, rangeURL, ""))
	if len(rangeAfter.Data) != 3 || rangeAfter.Data[0].Address != "10.131.0.19" || rangeAfter.Data[1].Address != target || rangeAfter.Data[2].Address != "10.131.0.21" {
		t.Fatalf("range availability after Unreserve = %#v", rangeAfter.Data)
	}
	var availableRows int
	if err := db.QueryRow("SELECT COUNT(*) FROM ip_allocations WHERE status='available'").Scan(&availableRows); err != nil || availableRows != 0 {
		t.Fatalf("persisted available rows = %d, error = %v", availableRows, err)
	}
}

func TestAllocationUnreserveAssignedRejectedAndPreserved(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	subnetService := service.NewSubnetService(subnetRepo)
	mux := setupUnreservationTestMux(subnetRepo, allocationRepo)
	subnet := createTestSubnet(t, subnetRepo, "10.132.0.0/24", nil, "assigned preservation")
	interfaceID := int64(42)
	allocationID := insertAllocation(t, db, subnet.ID, "10.132.0.20", domain.AllocationStatusAssigned, &interfaceID, "assigned description")

	before, err := subnetService.GetSubnet(ctx, subnet.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := serveUnreservationRequest(mux, http.MethodDelete, fmt.Sprintf("/api/v1/ip-allocations/%d", allocationID), "")
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var failure ipamhttp.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&failure); err != nil || failure.Error.Code != "IP_NOT_ASSIGNABLE" {
		t.Fatalf("error response = %#v, decode error = %v", failure, err)
	}

	var status, description string
	var persistedInterface sql.NullInt64
	if err := db.QueryRow("SELECT status, interface_id, description FROM ip_allocations WHERE id=$1", allocationID).Scan(&status, &persistedInterface, &description); err != nil {
		t.Fatalf("assigned allocation was not preserved: %v", err)
	}
	if status != "assigned" || !persistedInterface.Valid || persistedInterface.Int64 != interfaceID || description != "assigned description" {
		t.Fatalf("assigned state changed: status=%q interface=%v description=%q", status, persistedInterface, description)
	}
	after, err := subnetService.GetSubnet(ctx, subnet.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AssignedCount != before.AssignedCount || after.ReservedCount != before.ReservedCount || after.AvailableCount != before.AvailableCount {
		t.Fatalf("counts changed: before=%+v after=%+v", before, after)
	}
	exactURL := fmt.Sprintf("/api/v1/subnets/%d/available-ips?ip=10.132.0.20", subnet.ID)
	exact := decodeAvailableIPResponse(t, serveUnreservationRequest(mux, http.MethodGet, exactURL, ""))
	if len(exact.Data) != 0 {
		t.Fatalf("assigned address became available: %#v", exact.Data)
	}
}

func TestAllocationUnreserveMissingPostgresHTTP(t *testing.T) {
	db := setupTestDB(t)
	mux := setupUnreservationTestMux(postgres.NewSubnetRepository(db), postgres.NewAllocationRepository(db))
	response := serveUnreservationRequest(mux, http.MethodDelete, "/api/v1/ip-allocations/999999", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var failure ipamhttp.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&failure); err != nil || failure.Error.Code != "IP_ALLOCATION_NOT_FOUND" {
		t.Fatalf("error response = %#v, decode error = %v", failure, err)
	}
}

func TestAllocationUnreserveConcurrentSameAllocationSerializes(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	allocationService := service.NewAllocationService(allocationRepo)
	mux := setupUnreservationTestMux(subnetRepo, allocationRepo)
	subnet := createTestSubnet(t, subnetRepo, "10.133.0.0/24", nil, "same allocation concurrency")
	reserved, err := allocationService.ReserveAllocation(ctx, service.ReserveAllocationRequest{
		SubnetID: subnet.ID,
		Address:  subnet.CIDR.FirstUsableAddr(),
	})
	if err != nil {
		t.Fatal(err)
	}

	const pauseKey int64 = 0x4D3244554E524553
	if _, err := db.Exec(fmt.Sprintf(`
		CREATE FUNCTION test_pause_m2d_allocation_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM pg_advisory_xact_lock(%d);
			RETURN OLD;
		END $$;
		CREATE TRIGGER test_pause_m2d_allocation_delete_trigger
		BEFORE DELETE ON ip_allocations FOR EACH ROW EXECUTE FUNCTION test_pause_m2d_allocation_delete();
	`, pauseKey)); err != nil {
		t.Fatalf("failed to install Unreserve pause trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`
			DROP TRIGGER IF EXISTS test_pause_m2d_allocation_delete_trigger ON ip_allocations;
			DROP FUNCTION IF EXISTS test_pause_m2d_allocation_delete();
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

	path := fmt.Sprintf("/api/v1/ip-allocations/%d", reserved.ID)
	winnerDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		winnerDone <- serveUnreservationRequest(mux, http.MethodDelete, path, "")
	}()
	waitForPostgresCondition(t, "first production Unreserve paused after FOR UPDATE", func() (bool, error) {
		count, err := advisoryLockCount(db, false)
		return count > 0, err
	})

	loserDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		loserDone <- serveUnreservationRequest(mux, http.MethodDelete, path, "")
	}()
	waitForLockingQuery(t, db, "second Unreserve waiting on allocation row", "FOR UPDATE")
	select {
	case response := <-loserDone:
		t.Fatalf("second Unreserve completed before winner commit: status=%d body=%s", response.Code, response.Body.String())
	default:
	}

	if err := pauseHolder.Commit(); err != nil {
		t.Fatal(err)
	}
	winner := <-winnerDone
	loser := <-loserDone
	if winner.Code != http.StatusNoContent || winner.Body.Len() != 0 {
		t.Fatalf("winner status/body = %d/%q", winner.Code, winner.Body.String())
	}
	if loser.Code != http.StatusNotFound {
		t.Fatalf("loser status/body = %d/%s", loser.Code, loser.Body.String())
	}
	var failure ipamhttp.ErrorResponse
	if err := json.NewDecoder(loser.Body).Decode(&failure); err != nil || failure.Error.Code != "IP_ALLOCATION_NOT_FOUND" {
		t.Fatalf("loser response = %#v, decode error = %v", failure, err)
	}
	if got := countAddressRows(t, db, reserved.Address); got != 0 {
		t.Fatalf("final allocation row count = %d, want 0", got)
	}
}

func TestAllocationUnreserveDoesNotUseSubnetOrAdvisoryLock(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationService := service.NewAllocationService(postgres.NewAllocationRepository(db))
	subnet := createTestSubnet(t, subnetRepo, "10.134.0.0/24", nil, "no parent locks")
	reserved, err := allocationService.ReserveAllocation(ctx, service.ReserveAllocationRequest{
		SubnetID: subnet.ID,
		Address:  subnet.CIDR.FirstUsableAddr(),
	})
	if err != nil {
		t.Fatal(err)
	}

	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.Exec("SELECT pg_advisory_xact_lock($1)", postgres.SubnetCoordinationKey); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec("SELECT id FROM subnets WHERE id=$1 FOR UPDATE", subnet.ID); err != nil {
		t.Fatal(err)
	}

	unreserveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := allocationService.UnreserveAllocation(unreserveCtx, reserved.ID); err != nil {
		t.Fatalf("Unreserve waited on or failed because of Subnet/advisory locks: %v", err)
	}
}

func TestAllocationUnreserveDeleteRequiresOneAffectedRow(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	subnetRepo := postgres.NewSubnetRepository(db)
	allocationRepo := postgres.NewAllocationRepository(db)
	subnet := createTestSubnet(t, subnetRepo, "10.135.0.0/24", nil, "delete consistency")
	allocationID := insertAllocation(t, db, subnet.ID, "10.135.0.20", domain.AllocationStatusReserved, nil, "preserve on failure")

	tx, err := allocationRepo.BeginUnreservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	locked, err := tx.LockAllocation(ctx, allocationID)
	if err != nil || locked.ID != allocationID || locked.SubnetID != subnet.ID || locked.Address.String() != "10.135.0.20" ||
		locked.Status != domain.AllocationStatusReserved || locked.InterfaceID != nil || locked.Description != "preserve on failure" {
		t.Fatalf("locked allocation = %#v, error = %v", locked, err)
	}
	if err := tx.DeleteLockedAllocation(ctx, allocationID+999); err == nil {
		t.Fatal("DeleteLockedAllocation unexpectedly accepted zero affected rows")
	}
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		t.Fatal(err)
	}
	if got := countAddressRows(t, db, "10.135.0.20"); got != 1 {
		t.Fatalf("row count after failed delete = %d, want 1", got)
	}
}
