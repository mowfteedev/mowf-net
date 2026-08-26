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
	ipamhttp.NewSubnetHandler(service.NewSubnetService(subnetRepo)).RegisterRoutes(mux)
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

func waitForX1AdvisoryWaiter(t *testing.T, db *sql.DB, holderPID int, queryFragment string) {
	t.Helper()
	waitForPostgresCondition(t, fmt.Sprintf("X1 query %q waiting on advisory lock held by backend %d", queryFragment, holderPID), func() (bool, error) {
		var waiting bool
		err := db.QueryRow(`
			SELECT EXISTS (
				SELECT 1
				FROM pg_locks waiter
				JOIN pg_locks holder
				  ON holder.pid = $1
				 AND holder.locktype = 'advisory'
				 AND holder.granted
				 AND waiter.database IS NOT DISTINCT FROM holder.database
				 AND waiter.classid IS NOT DISTINCT FROM holder.classid
				 AND waiter.objid IS NOT DISTINCT FROM holder.objid
				 AND waiter.objsubid IS NOT DISTINCT FROM holder.objsubid
				JOIN pg_stat_activity activity ON activity.pid = waiter.pid
				WHERE waiter.locktype = 'advisory'
				  AND NOT waiter.granted
				  AND waiter.pid <> holder.pid
				  AND activity.datname = current_database()
				  AND activity.wait_event_type = 'Lock'
				  AND activity.wait_event = 'advisory'
				  AND position($2 in activity.query) > 0
			)
		`, holderPID, queryFragment).Scan(&waiting)
		return waiting, err
	})
}

func waitForX2QueryBlockedByPID(t *testing.T, db *sql.DB, holderPID int, queryFragment string) {
	t.Helper()
	waitForPostgresCondition(t, fmt.Sprintf("X2 query %q blocked by backend %d", queryFragment, holderPID), func() (bool, error) {
		var waiting bool
		err := db.QueryRow(`
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity waiter
				JOIN pg_stat_activity holder
				  ON holder.pid = $1
				 AND holder.datname = current_database()
				WHERE waiter.pid <> holder.pid
				  AND waiter.datname = current_database()
				  AND waiter.state = 'active'
				  AND waiter.wait_event_type = 'Lock'
				  AND holder.pid = ANY(pg_blocking_pids(waiter.pid))
				  AND position($2 in waiter.query) > 0
			)
		`, holderPID, queryFragment).Scan(&waiting)
		return waiting, err
	})
}

func receiveX1HTTPResponse(t *testing.T, description string, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case response := <-done:
		return response
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func countX1SubnetAllocations(t *testing.T, db *sql.DB, subnetID int64) int {
	t.Helper()
	var subnetCount, totalCount int
	if err := db.QueryRow(`
		SELECT COUNT(*) FILTER (WHERE subnet_id=$1), COUNT(*)
		FROM ip_allocations
	`, subnetID).Scan(&subnetCount, &totalCount); err != nil {
		t.Fatalf("count X1 subnet allocations: %v", err)
	}
	if subnetCount != totalCount {
		t.Fatalf("X1 unexpected allocation rows outside subnet %d: subnet=%d total=%d", subnetID, subnetCount, totalCount)
	}
	return subnetCount
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

func TestM2Harden01UnreserveVsSubnetResizeDeterministicOrderings(t *testing.T) {
	const (
		originalCIDR = "10.136.0.0/24"
		resizedCIDR  = "10.136.0.0/25"
		target       = "10.136.0.200"
	)

	original, err := domain.ParseCIDR(originalCIDR)
	if err != nil {
		t.Fatal(err)
	}
	resized, err := domain.ParseCIDR(resizedCIDR)
	if err != nil {
		t.Fatal(err)
	}
	if usable, err := original.IsUsableIPString(target); err != nil || !usable {
		t.Fatalf("target must be usable in original CIDR: usable=%v error=%v", usable, err)
	}
	if usable, err := resized.IsUsableIPString(target); err != nil || usable {
		t.Fatalf("target must be outside resized usable range: usable=%v error=%v", usable, err)
	}

	t.Run("resize observes allocation before unreserve commit", func(t *testing.T) {
		db := setupTestDB(t)
		ctx := context.Background()
		subnetRepo := postgres.NewSubnetRepository(db)
		allocationRepo := postgres.NewAllocationRepository(db)
		mux := setupUnreservationTestMux(subnetRepo, allocationRepo)
		subnet := createTestSubnet(t, subnetRepo, originalCIDR, nil, "X1 uncommitted Unreserve")
		allocationID := insertAllocation(t, db, subnet.ID, target, domain.AllocationStatusReserved, nil, "X1 boundary allocation")

		pauseKey := time.Now().UnixNano()
		if pauseKey == postgres.SubnetCoordinationKey {
			pauseKey++
		}
		functionName := fmt.Sprintf("x1_pause_delete_fn_%d", pauseKey)
		triggerName := fmt.Sprintf("x1_pause_delete_trg_%d", pauseKey)
		if _, err := db.Exec(fmt.Sprintf(`
			CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				PERFORM pg_advisory_xact_lock(%d);
				RETURN OLD;
			END $$;
			CREATE TRIGGER %s
			BEFORE DELETE ON ip_allocations
			FOR EACH ROW WHEN (OLD.id = %d)
			EXECUTE FUNCTION %s();
		`, functionName, pauseKey, triggerName, allocationID, functionName)); err != nil {
			t.Fatalf("install X1 Unreserve pause trigger: %v", err)
		}
		t.Cleanup(func() {
			_, _ = db.Exec(fmt.Sprintf(`
				DROP TRIGGER IF EXISTS %s ON ip_allocations;
				DROP FUNCTION IF EXISTS %s();
			`, triggerName, functionName))
		})

		pauseHolder, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		pauseHolderDone := false
		t.Cleanup(func() {
			if !pauseHolderDone {
				_ = pauseHolder.Rollback()
			}
		})
		var pauseHolderPID int
		if err := pauseHolder.QueryRow("SELECT pg_backend_pid()").Scan(&pauseHolderPID); err != nil {
			t.Fatal(err)
		}
		if _, err := pauseHolder.Exec("SELECT pg_advisory_xact_lock($1)", pauseKey); err != nil {
			t.Fatal(err)
		}

		unreserveDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			unreserveDone <- serveUnreservationRequest(mux, http.MethodDelete, fmt.Sprintf("/api/v1/ip-allocations/%d", allocationID), "")
		}()
		waitForX1AdvisoryWaiter(t, db, pauseHolderPID, "DELETE FROM ip_allocations")

		resizeDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			resizeDone <- serveUnreservationRequest(mux, http.MethodPatch, fmt.Sprintf("/api/v1/subnets/%d", subnet.ID), fmt.Sprintf(`{"cidr":%q}`, resizedCIDR))
		}()
		resizeResponse := receiveX1HTTPResponse(t, "Resize conflict before releasing Unreserve", resizeDone)
		if resizeResponse.Code != http.StatusConflict {
			t.Fatalf("Resize status/body = %d/%s, want 409", resizeResponse.Code, resizeResponse.Body.String())
		}
		var conflict ipamhttp.ErrorResponse
		if err := json.NewDecoder(resizeResponse.Body).Decode(&conflict); err != nil || conflict.Error.Code != "SUBNET_RESIZE_CONFLICT" {
			t.Fatalf("Resize conflict response = %#v, decode error = %v", conflict, err)
		}
		whilePaused, err := subnetRepo.GetByID(ctx, subnet.ID)
		if err != nil {
			t.Fatal(err)
		}
		if whilePaused.CIDR.CIDR() != originalCIDR || whilePaused.ReservedCount != 1 || countX1SubnetAllocations(t, db, subnet.ID) != 1 {
			t.Fatalf("state while Unreserve paused = cidr:%s reserved:%d rows:%d", whilePaused.CIDR.CIDR(), whilePaused.ReservedCount, countX1SubnetAllocations(t, db, subnet.ID))
		}
		select {
		case response := <-unreserveDone:
			t.Fatalf("Unreserve completed before pause release: status=%d body=%s", response.Code, response.Body.String())
		default:
		}

		if err := pauseHolder.Commit(); err != nil {
			t.Fatal(err)
		}
		pauseHolderDone = true
		unreserveResponse := receiveX1HTTPResponse(t, "Unreserve after pause release", unreserveDone)
		if unreserveResponse.Code != http.StatusNoContent || unreserveResponse.Body.Len() != 0 {
			t.Fatalf("Unreserve status/body = %d/%q, want 204/empty", unreserveResponse.Code, unreserveResponse.Body.String())
		}
		finalState, err := subnetRepo.GetByID(ctx, subnet.ID)
		if err != nil {
			t.Fatal(err)
		}
		if finalState.CIDR.CIDR() != originalCIDR || finalState.ReservedCount != 0 || countX1SubnetAllocations(t, db, subnet.ID) != 0 || countAddressRows(t, db, target) != 0 {
			t.Fatalf("final state = cidr:%s reserved:%d rows:%d target_rows:%d", finalState.CIDR.CIDR(), finalState.ReservedCount, countX1SubnetAllocations(t, db, subnet.ID), countAddressRows(t, db, target))
		}
	})

	t.Run("unreserve commits before resize scan", func(t *testing.T) {
		db := setupTestDB(t)
		ctx := context.Background()
		subnetRepo := postgres.NewSubnetRepository(db)
		allocationRepo := postgres.NewAllocationRepository(db)
		mux := setupUnreservationTestMux(subnetRepo, allocationRepo)
		subnet := createTestSubnet(t, subnetRepo, originalCIDR, nil, "X1 committed Unreserve")
		allocationID := insertAllocation(t, db, subnet.ID, target, domain.AllocationStatusReserved, nil, "X1 boundary allocation")

		coordinationHolder, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		coordinationHolderDone := false
		t.Cleanup(func() {
			if !coordinationHolderDone {
				_ = coordinationHolder.Rollback()
			}
		})
		var coordinationHolderPID int
		if err := coordinationHolder.QueryRow("SELECT pg_backend_pid()").Scan(&coordinationHolderPID); err != nil {
			t.Fatal(err)
		}
		if _, err := coordinationHolder.Exec("SELECT pg_advisory_xact_lock($1)", postgres.SubnetCoordinationKey); err != nil {
			t.Fatal(err)
		}

		resizeDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			resizeDone <- serveUnreservationRequest(mux, http.MethodPatch, fmt.Sprintf("/api/v1/subnets/%d", subnet.ID), fmt.Sprintf(`{"cidr":%q}`, resizedCIDR))
		}()
		waitForX1AdvisoryWaiter(t, db, coordinationHolderPID, "pg_advisory_xact_lock")
		select {
		case response := <-resizeDone:
			t.Fatalf("Resize completed before coordination lock release: status=%d body=%s", response.Code, response.Body.String())
		default:
		}

		unreserveResponse := serveUnreservationRequest(mux, http.MethodDelete, fmt.Sprintf("/api/v1/ip-allocations/%d", allocationID), "")
		if unreserveResponse.Code != http.StatusNoContent || unreserveResponse.Body.Len() != 0 {
			t.Fatalf("Unreserve status/body = %d/%q, want 204/empty", unreserveResponse.Code, unreserveResponse.Body.String())
		}
		if countX1SubnetAllocations(t, db, subnet.ID) != 0 || countAddressRows(t, db, target) != 0 {
			t.Fatalf("Unreserve was not committed before release: rows=%d target_rows=%d", countX1SubnetAllocations(t, db, subnet.ID), countAddressRows(t, db, target))
		}
		select {
		case response := <-resizeDone:
			t.Fatalf("Resize completed while coordination lock remained held: status=%d body=%s", response.Code, response.Body.String())
		default:
		}

		if err := coordinationHolder.Commit(); err != nil {
			t.Fatal(err)
		}
		coordinationHolderDone = true
		resizeResponse := receiveX1HTTPResponse(t, "Resize after committed Unreserve", resizeDone)
		if resizeResponse.Code != http.StatusOK {
			t.Fatalf("Resize status/body = %d/%s, want 200", resizeResponse.Code, resizeResponse.Body.String())
		}
		var resizedResponse struct {
			Data service.SubnetDTO `json:"data"`
		}
		if err := json.NewDecoder(resizeResponse.Body).Decode(&resizedResponse); err != nil {
			t.Fatalf("decode successful Resize response: %v", err)
		}
		if resizedResponse.Data.ID != subnet.ID || resizedResponse.Data.CIDR != resizedCIDR || resizedResponse.Data.ReservedCount != 0 {
			t.Fatalf("successful Resize response = %+v", resizedResponse.Data)
		}
		finalState, err := subnetRepo.GetByID(ctx, subnet.ID)
		if err != nil {
			t.Fatal(err)
		}
		if finalState.CIDR.CIDR() != resizedCIDR || finalState.ReservedCount != 0 || countX1SubnetAllocations(t, db, subnet.ID) != 0 || countAddressRows(t, db, target) != 0 {
			t.Fatalf("final state = cidr:%s reserved:%d rows:%d target_rows:%d", finalState.CIDR.CIDR(), finalState.ReservedCount, countX1SubnetAllocations(t, db, subnet.ID), countAddressRows(t, db, target))
		}
	})
}

func TestM2Harden02UnreserveVsSubnetDeleteDeterministicOrderings(t *testing.T) {
	const (
		cidr                    = "10.137.0.0/24"
		target                  = "10.137.0.20"
		subnetHasAllocationsMsg = "The subnet cannot be deleted while allocations exist."
	)

	t.Run("subnet delete observes allocation before unreserve commit", func(t *testing.T) {
		db := setupTestDB(t)
		ctx := context.Background()
		subnetRepo := postgres.NewSubnetRepository(db)
		allocationRepo := postgres.NewAllocationRepository(db)
		mux := setupUnreservationTestMux(subnetRepo, allocationRepo)
		subnet := createTestSubnet(t, subnetRepo, cidr, nil, "X2 uncommitted Unreserve")
		allocationID := insertAllocation(t, db, subnet.ID, target, domain.AllocationStatusReserved, nil, "X2 child allocation")

		pauseKey := time.Now().UnixNano()
		if pauseKey == postgres.SubnetCoordinationKey {
			pauseKey++
		}
		functionName := fmt.Sprintf("x2_pause_delete_fn_%d", pauseKey)
		triggerName := fmt.Sprintf("x2_pause_delete_trg_%d", pauseKey)
		if _, err := db.Exec(fmt.Sprintf(`
			CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				PERFORM pg_advisory_xact_lock(%d);
				RETURN OLD;
			END $$;
			CREATE TRIGGER %s
			BEFORE DELETE ON ip_allocations
			FOR EACH ROW WHEN (OLD.id = %d)
			EXECUTE FUNCTION %s();
		`, functionName, pauseKey, triggerName, allocationID, functionName)); err != nil {
			t.Fatalf("install X2 Unreserve pause trigger: %v", err)
		}
		t.Cleanup(func() {
			_, _ = db.Exec(fmt.Sprintf(`
				DROP TRIGGER IF EXISTS %s ON ip_allocations;
				DROP FUNCTION IF EXISTS %s();
			`, triggerName, functionName))
		})

		pauseHolder, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		pauseHolderDone := false
		t.Cleanup(func() {
			if !pauseHolderDone {
				_ = pauseHolder.Rollback()
			}
		})
		var pauseHolderPID int
		if err := pauseHolder.QueryRow("SELECT pg_backend_pid()").Scan(&pauseHolderPID); err != nil {
			t.Fatal(err)
		}
		if _, err := pauseHolder.Exec("SELECT pg_advisory_xact_lock($1)", pauseKey); err != nil {
			t.Fatal(err)
		}

		unreserveDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			unreserveDone <- serveUnreservationRequest(mux, http.MethodDelete, fmt.Sprintf("/api/v1/ip-allocations/%d", allocationID), "")
		}()
		waitForX1AdvisoryWaiter(t, db, pauseHolderPID, "DELETE FROM ip_allocations")
		select {
		case response := <-unreserveDone:
			t.Fatalf("Unreserve completed before X2 pause release: status=%d body=%s", response.Code, response.Body.String())
		default:
		}

		subnetDeleteDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			subnetDeleteDone <- serveUnreservationRequest(mux, http.MethodDelete, fmt.Sprintf("/api/v1/subnets/%d", subnet.ID), "")
		}()
		subnetDeleteResponse := receiveX1HTTPResponse(t, "Subnet Delete conflict while Unreserve remains paused", subnetDeleteDone)
		if subnetDeleteResponse.Code != http.StatusConflict {
			t.Fatalf("Subnet Delete status/body = %d/%s, want 409", subnetDeleteResponse.Code, subnetDeleteResponse.Body.String())
		}
		var conflict ipamhttp.ErrorResponse
		if err := json.NewDecoder(subnetDeleteResponse.Body).Decode(&conflict); err != nil ||
			conflict.Error.Code != "SUBNET_HAS_ALLOCATIONS" || conflict.Error.Message != subnetHasAllocationsMsg {
			t.Fatalf("Subnet Delete conflict response = %#v, decode error = %v", conflict, err)
		}

		whilePaused, err := subnetRepo.GetByID(ctx, subnet.ID)
		if err != nil {
			t.Fatalf("Subnet missing while Unreserve paused: %v", err)
		}
		if whilePaused.CIDR.CIDR() != cidr || whilePaused.ReservedCount != 1 ||
			countX1SubnetAllocations(t, db, subnet.ID) != 1 || countAddressRows(t, db, target) != 1 {
			t.Fatalf("state while Unreserve paused = cidr:%s reserved:%d rows:%d target_rows:%d",
				whilePaused.CIDR.CIDR(), whilePaused.ReservedCount, countX1SubnetAllocations(t, db, subnet.ID), countAddressRows(t, db, target))
		}
		select {
		case response := <-unreserveDone:
			t.Fatalf("Unreserve completed before barrier release after Subnet Delete: status=%d body=%s", response.Code, response.Body.String())
		default:
		}

		if err := pauseHolder.Commit(); err != nil {
			t.Fatal(err)
		}
		pauseHolderDone = true
		unreserveResponse := receiveX1HTTPResponse(t, "Unreserve after X2 pause release", unreserveDone)
		if unreserveResponse.Code != http.StatusNoContent || unreserveResponse.Body.Len() != 0 {
			t.Fatalf("Unreserve status/body = %d/%q, want 204/empty", unreserveResponse.Code, unreserveResponse.Body.String())
		}

		finalState, err := subnetRepo.GetByID(ctx, subnet.ID)
		if err != nil {
			t.Fatalf("Subnet missing after completed Unreserve: %v", err)
		}
		if finalState.CIDR.CIDR() != cidr || finalState.ReservedCount != 0 ||
			countX1SubnetAllocations(t, db, subnet.ID) != 0 || countAddressRows(t, db, target) != 0 {
			t.Fatalf("final state = cidr:%s reserved:%d rows:%d target_rows:%d",
				finalState.CIDR.CIDR(), finalState.ReservedCount, countX1SubnetAllocations(t, db, subnet.ID), countAddressRows(t, db, target))
		}
	})

	t.Run("unreserve commits before subnet delete allocation check", func(t *testing.T) {
		db := setupTestDB(t)
		ctx := context.Background()
		subnetRepo := postgres.NewSubnetRepository(db)
		allocationRepo := postgres.NewAllocationRepository(db)
		mux := setupUnreservationTestMux(subnetRepo, allocationRepo)
		subnet := createTestSubnet(t, subnetRepo, cidr, nil, "X2 committed Unreserve")
		allocationID := insertAllocation(t, db, subnet.ID, target, domain.AllocationStatusReserved, nil, "X2 child allocation")

		parentHolder, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		parentHolderDone := false
		t.Cleanup(func() {
			if !parentHolderDone {
				_ = parentHolder.Rollback()
			}
		})
		var parentHolderPID int
		var lockedSubnetID int64
		if err := parentHolder.QueryRow("SELECT pg_backend_pid()").Scan(&parentHolderPID); err != nil {
			t.Fatal(err)
		}
		if err := parentHolder.QueryRow("SELECT id FROM subnets WHERE id = $1 FOR UPDATE", subnet.ID).Scan(&lockedSubnetID); err != nil {
			t.Fatal(err)
		}
		if lockedSubnetID != subnet.ID {
			t.Fatalf("locked Subnet ID = %d, want %d", lockedSubnetID, subnet.ID)
		}

		subnetDeleteDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			subnetDeleteDone <- serveUnreservationRequest(mux, http.MethodDelete, fmt.Sprintf("/api/v1/subnets/%d", subnet.ID), "")
		}()
		waitForX2QueryBlockedByPID(t, db, parentHolderPID, "SELECT id FROM subnets WHERE id = $1 FOR UPDATE")
		select {
		case response := <-subnetDeleteDone:
			t.Fatalf("Subnet Delete completed before parent lock release: status=%d body=%s", response.Code, response.Body.String())
		default:
		}

		unreserveDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			unreserveDone <- serveUnreservationRequest(mux, http.MethodDelete, fmt.Sprintf("/api/v1/ip-allocations/%d", allocationID), "")
		}()
		unreserveResponse := receiveX1HTTPResponse(t, "Unreserve while Subnet Delete is parent-lock blocked", unreserveDone)
		if unreserveResponse.Code != http.StatusNoContent || unreserveResponse.Body.Len() != 0 {
			t.Fatalf("Unreserve status/body = %d/%q, want 204/empty", unreserveResponse.Code, unreserveResponse.Body.String())
		}

		var parentCount, subnetAllocationCount, targetAllocationCount, totalAllocationCount int
		if err := db.QueryRow(`
			SELECT
				(SELECT COUNT(*) FROM subnets WHERE id = $1),
				(SELECT COUNT(*) FROM ip_allocations WHERE subnet_id = $1),
				(SELECT COUNT(*) FROM ip_allocations WHERE id = $2),
				(SELECT COUNT(*) FROM ip_allocations)
		`, subnet.ID, allocationID).Scan(&parentCount, &subnetAllocationCount, &targetAllocationCount, &totalAllocationCount); err != nil {
			t.Fatalf("inspect X2 state before parent release: %v", err)
		}
		if parentCount != 1 || subnetAllocationCount != 0 || targetAllocationCount != 0 || totalAllocationCount != 0 {
			t.Fatalf("state before parent release = parent:%d subnet_rows:%d target_rows:%d total_rows:%d",
				parentCount, subnetAllocationCount, targetAllocationCount, totalAllocationCount)
		}
		select {
		case response := <-subnetDeleteDone:
			t.Fatalf("Subnet Delete completed after Unreserve but before parent lock release: status=%d body=%s", response.Code, response.Body.String())
		default:
		}

		if err := parentHolder.Commit(); err != nil {
			t.Fatal(err)
		}
		parentHolderDone = true
		subnetDeleteResponse := receiveX1HTTPResponse(t, "Subnet Delete after committed Unreserve", subnetDeleteDone)
		if subnetDeleteResponse.Code != http.StatusNoContent || subnetDeleteResponse.Body.Len() != 0 {
			t.Fatalf("Subnet Delete status/body = %d/%q, want 204/empty", subnetDeleteResponse.Code, subnetDeleteResponse.Body.String())
		}

		if err := db.QueryRow(`
			SELECT
				(SELECT COUNT(*) FROM subnets WHERE id = $1),
				(SELECT COUNT(*) FROM ip_allocations WHERE subnet_id = $1),
				(SELECT COUNT(*) FROM ip_allocations WHERE id = $2),
				(SELECT COUNT(*) FROM ip_allocations)
		`, subnet.ID, allocationID).Scan(&parentCount, &subnetAllocationCount, &targetAllocationCount, &totalAllocationCount); err != nil {
			t.Fatalf("inspect final X2 state: %v", err)
		}
		if parentCount != 0 || subnetAllocationCount != 0 || targetAllocationCount != 0 || totalAllocationCount != 0 {
			t.Fatalf("final state = parent:%d subnet_rows:%d target_rows:%d total_rows:%d",
				parentCount, subnetAllocationCount, targetAllocationCount, totalAllocationCount)
		}
	})
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
