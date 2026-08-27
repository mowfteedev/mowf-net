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
	"time"

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

type x3DeleteBarrier struct {
	holder      *sql.Tx
	holderPID   int
	pauseKey    int64
	triggerName string
	released    bool
}

type x3InsertWaitEvidence struct {
	PID                int
	WaitEvent          string
	UngrantedLockTypes string
	BlockingPIDs       string
}

func assertX3UniqueAddressConstraint(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM pg_constraint constraint_record
		JOIN pg_class table_record ON table_record.oid = constraint_record.conrelid
		JOIN pg_index index_record ON index_record.indexrelid = constraint_record.conindid
		WHERE table_record.oid = 'ip_allocations'::regclass
		  AND constraint_record.conname = 'ip_allocations_address_uq'
		  AND constraint_record.contype = 'u'
		  AND NOT constraint_record.condeferrable
		  AND constraint_record.convalidated
		  AND index_record.indisunique
		  AND index_record.indisvalid
		  AND index_record.indisready
		  AND pg_get_constraintdef(constraint_record.oid) = 'UNIQUE (address)'
	`).Scan(&count); err != nil {
		t.Fatalf("inspect X3 UNIQUE(address) authority: %v", err)
	}
	if count != 1 {
		t.Fatalf("active ip_allocations_address_uq UNIQUE(address) constraint count = %d, want 1", count)
	}
}

func installX3AfterDeleteBarrier(t *testing.T, db *sql.DB, allocationID int64, forceRollback bool) *x3DeleteBarrier {
	t.Helper()
	nonce := time.Now().UnixNano()
	mode := "commit"
	afterWait := "RETURN OLD;"
	if forceRollback {
		mode = "rollback"
		afterWait = "RAISE EXCEPTION 'M2-HARDEN-03 X3 forced rollback after DELETE'; RETURN OLD;"
	}
	functionName := fmt.Sprintf("x3_after_delete_%s_fn_%d", mode, nonce)
	triggerName := fmt.Sprintf("x3_after_delete_%s_trg_%d", mode, nonce)
	pauseKey := -nonce

	if _, err := db.Exec(fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM pg_advisory_xact_lock(%d);
			%s
		END $$;
		CREATE TRIGGER %s
		AFTER DELETE ON ip_allocations
		FOR EACH ROW WHEN (OLD.id = %d)
		EXECUTE FUNCTION %s();
	`, functionName, pauseKey, afterWait, triggerName, allocationID, functionName)); err != nil {
		t.Fatalf("install X3 AFTER DELETE barrier: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(fmt.Sprintf(`
			DROP TRIGGER IF EXISTS %s ON ip_allocations;
			DROP FUNCTION IF EXISTS %s();
		`, triggerName, functionName)); err != nil {
			t.Errorf("clean up X3 AFTER DELETE barrier: %v", err)
		}
	})

	var triggerCount int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM pg_trigger trigger_record
		WHERE trigger_record.tgrelid = 'ip_allocations'::regclass
		  AND trigger_record.tgname = $1
		  AND NOT trigger_record.tgisinternal
		  AND trigger_record.tgenabled <> 'D'
		  AND (trigger_record.tgtype::integer & 1) = 1
		  AND (trigger_record.tgtype::integer & 2) = 0
		  AND (trigger_record.tgtype::integer & 8) = 8
		  AND (trigger_record.tgtype::integer & (4 | 16 | 32 | 64)) = 0
		  AND position($2 in pg_get_expr(trigger_record.tgqual, trigger_record.tgrelid)) > 0
	`, triggerName, fmt.Sprint(allocationID)).Scan(&triggerCount); err != nil {
		t.Fatalf("inspect X3 AFTER DELETE barrier: %v", err)
	}
	if triggerCount != 1 {
		t.Fatalf("enabled exact-row AFTER DELETE trigger count = %d, want 1", triggerCount)
	}

	holder, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin X3 advisory holder: %v", err)
	}
	barrier := &x3DeleteBarrier{
		holder:      holder,
		pauseKey:    pauseKey,
		triggerName: triggerName,
	}
	t.Cleanup(func() {
		if !barrier.released {
			if err := barrier.holder.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				t.Errorf("roll back X3 advisory holder: %v", err)
			}
		}
	})
	if err := holder.QueryRow("SELECT pg_backend_pid()").Scan(&barrier.holderPID); err != nil {
		t.Fatalf("read X3 advisory holder PID: %v", err)
	}
	if _, err := holder.Exec("SELECT pg_advisory_xact_lock($1)", pauseKey); err != nil {
		t.Fatalf("acquire X3 advisory barrier: %v", err)
	}
	return barrier
}

func (barrier *x3DeleteBarrier) release(t *testing.T) {
	t.Helper()
	if err := barrier.holder.Commit(); err != nil {
		t.Fatalf("release X3 advisory barrier: %v", err)
	}
	barrier.released = true
}

func waitForX3AdvisoryWaiterPID(t *testing.T, db *sql.DB, holderPID int) int {
	t.Helper()
	var unreservePID int
	waitForPostgresCondition(t, fmt.Sprintf("X3 production DELETE waiting on AFTER DELETE advisory holder %d", holderPID), func() (bool, error) {
		var observedPID int
		err := db.QueryRow(`
			SELECT activity.pid
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
			JOIN pg_stat_activity holder_activity ON holder_activity.pid = holder.pid
			WHERE waiter.locktype = 'advisory'
			  AND NOT waiter.granted
			  AND waiter.pid <> holder.pid
			  AND activity.datname = current_database()
			  AND holder_activity.datname = current_database()
			  AND activity.state = 'active'
			  AND activity.wait_event_type = 'Lock'
			  AND activity.wait_event = 'advisory'
			  AND holder.pid = ANY(pg_blocking_pids(activity.pid))
			  AND position('DELETE FROM ip_allocations' in activity.query) > 0
			ORDER BY activity.pid
			LIMIT 1
		`, holderPID).Scan(&observedPID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		unreservePID = observedPID
		return true, nil
	})
	return unreservePID
}

func waitForX3InsertBlockedByPID(t *testing.T, db *sql.DB, unreservePID int) x3InsertWaitEvidence {
	t.Helper()
	var evidence x3InsertWaitEvidence
	waitForPostgresCondition(t, fmt.Sprintf("X3 production INSERT blocked by Unreserve backend %d", unreservePID), func() (bool, error) {
		var observed x3InsertWaitEvidence
		err := db.QueryRow(`
			SELECT activity.pid,
			       activity.wait_event,
			       COALESCE((
			           SELECT string_agg(DISTINCT waiting_lock.locktype, ',' ORDER BY waiting_lock.locktype)
			           FROM pg_locks waiting_lock
			           WHERE waiting_lock.pid = activity.pid
			             AND NOT waiting_lock.granted
			       ), ''),
			       array_to_string(pg_blocking_pids(activity.pid), ',')
			FROM pg_stat_activity activity
			JOIN pg_stat_activity blocker
			  ON blocker.pid = $1
			 AND blocker.datname = current_database()
			WHERE activity.pid <> blocker.pid
			  AND activity.datname = current_database()
			  AND activity.state = 'active'
			  AND activity.wait_event_type = 'Lock'
			  AND blocker.state = 'active'
			  AND blocker.wait_event_type = 'Lock'
			  AND blocker.wait_event = 'advisory'
			  AND blocker.pid = ANY(pg_blocking_pids(activity.pid))
			  AND position('INSERT INTO ip_allocations' in activity.query) > 0
			ORDER BY activity.pid
			LIMIT 1
		`, unreservePID).Scan(&observed.PID, &observed.WaitEvent, &observed.UngrantedLockTypes, &observed.BlockingPIDs)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		evidence = observed
		return true, nil
	})
	return evidence
}

func assertX3HTTPPending(t *testing.T, description string, done <-chan *httptest.ResponseRecorder) {
	t.Helper()
	select {
	case response := <-done:
		t.Fatalf("%s completed before X3 barrier release: status=%d body=%s", description, response.Code, response.Body.String())
	default:
	}
}

func receiveX3HTTPResponse(t *testing.T, description string, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case response := <-done:
		return response
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func assertX3CommittedAllocation(t *testing.T, db *sql.DB, allocationID, subnetID int64, address, status, description string) {
	t.Helper()
	var gotStatus, gotAddress, gotDescription string
	var gotSubnetID int64
	var interfaceID sql.NullInt64
	if err := db.QueryRow(`
		SELECT subnet_id, host(address), status, interface_id, description
		FROM ip_allocations
		WHERE id = $1
	`, allocationID).Scan(&gotSubnetID, &gotAddress, &gotStatus, &interfaceID, &gotDescription); err != nil {
		t.Fatalf("read committed X3 allocation %d: %v", allocationID, err)
	}
	if gotSubnetID != subnetID || gotAddress != address || gotStatus != status || interfaceID.Valid || gotDescription != description {
		t.Fatalf("committed X3 allocation = subnet:%d address:%s status:%s interface:%v description:%q; want subnet:%d address:%s status:%s NULL interface description:%q", gotSubnetID, gotAddress, gotStatus, interfaceID, gotDescription, subnetID, address, status, description)
	}
}

func assertX3CommittedOldAllocation(t *testing.T, db *sql.DB, allocationID, subnetID int64, address, description string) {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM ip_allocations WHERE id=$1", allocationID).Scan(&count); err != nil {
		t.Fatalf("count committed X3 old allocation %d: %v", allocationID, err)
	}
	if count != 1 {
		t.Fatalf("committed X3 old allocation count = %d, want 1", count)
	}
	assertX3CommittedAllocation(t, db, allocationID, subnetID, address, string(domain.AllocationStatusReserved), description)
}

func assertX3ResponseError(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantCode, wantMessage string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("HTTP status = %d, body = %s; want %d", response.Code, response.Body.String(), wantStatus)
	}
	var failure ipamhttp.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
		t.Fatalf("decode HTTP error response: %v", err)
	}
	if failure.Error.Code != wantCode || failure.Error.Message != wantMessage {
		t.Fatalf("HTTP error = %#v; want code %q message %q", failure.Error, wantCode, wantMessage)
	}
}

func TestM2Harden03ReserveVsUnreserveSameAddressDeterministicOrderings(t *testing.T) {
	const (
		cidr            = "10.140.0.0/24"
		target          = "10.140.0.20"
		replacementDesc = "X3 replacement reservation"
		originalDesc    = "X3 original reservation"
	)

	t.Run("unreserve commit releases same-address reserve", func(t *testing.T) {
		db := setupTestDB(t)
		ctx := context.Background()
		subnetRepo := postgres.NewSubnetRepository(db)
		subnetService := service.NewSubnetService(subnetRepo)
		allocationRepo := postgres.NewAllocationRepository(db)
		mux := setupUnreservationTestMux(subnetRepo, allocationRepo)
		subnet := createTestSubnet(t, subnetRepo, cidr, nil, "X3 commit")
		oldID := insertAllocation(t, db, subnet.ID, target, domain.AllocationStatusReserved, nil, originalDesc)
		assertX3UniqueAddressConstraint(t, db)

		barrier := installX3AfterDeleteBarrier(t, db, oldID, false)
		unreserveDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			unreserveDone <- serveUnreservationRequest(mux, http.MethodDelete, fmt.Sprintf("/api/v1/ip-allocations/%d", oldID), "")
		}()
		unreservePID := waitForX3AdvisoryWaiterPID(t, db, barrier.holderPID)
		if unreservePID == barrier.holderPID {
			t.Fatalf("Unreserve PID %d is advisory holder PID", unreservePID)
		}

		reserveDone := make(chan *httptest.ResponseRecorder, 1)
		body := fmt.Sprintf(`{"subnet_id":%d,"address":%q,"description":%q}`, subnet.ID, target, replacementDesc)
		go func() {
			reserveDone <- serveUnreservationRequest(mux, http.MethodPost, "/api/v1/ip-allocations", body)
		}()
		evidence := waitForX3InsertBlockedByPID(t, db, unreservePID)
		if evidence.PID == unreservePID || evidence.WaitEvent == "" {
			t.Fatalf("invalid X3 INSERT wait evidence: %+v", evidence)
		}
		t.Logf("X3 commit evidence: holderPID=%d unreservePID=%d reservePID=%d wait_event=%s ungranted_locks=%q blocking_pids=%q", barrier.holderPID, unreservePID, evidence.PID, evidence.WaitEvent, evidence.UngrantedLockTypes, evidence.BlockingPIDs)
		assertX3HTTPPending(t, "Unreserve", unreserveDone)
		assertX3HTTPPending(t, "Reserve", reserveDone)
		if got := countAddressRows(t, db, target); got != 1 {
			t.Fatalf("committed target row count while both transactions wait = %d, want 1", got)
		}
		assertX3CommittedOldAllocation(t, db, oldID, subnet.ID, target, originalDesc)

		barrier.release(t)
		unreserveResponse := receiveX3HTTPResponse(t, "Unreserve commit response", unreserveDone)
		reserveResponse := receiveX3HTTPResponse(t, "Reserve replacement response", reserveDone)
		if unreserveResponse.Code != http.StatusNoContent || unreserveResponse.Body.Len() != 0 {
			t.Fatalf("Unreserve response = status %d body %q; want 204 empty", unreserveResponse.Code, unreserveResponse.Body.String())
		}
		if reserveResponse.Code != http.StatusCreated {
			t.Fatalf("Reserve response = status %d body %s; want 201", reserveResponse.Code, reserveResponse.Body.String())
		}
		var created struct {
			Data service.AllocationDTO `json:"data"`
		}
		if err := json.NewDecoder(reserveResponse.Body).Decode(&created); err != nil {
			t.Fatalf("decode replacement response: %v", err)
		}
		if created.Data.ID <= 0 || created.Data.ID == oldID || created.Data.SubnetID != subnet.ID || created.Data.Address != target || created.Data.Status != string(domain.AllocationStatusReserved) || created.Data.InterfaceID != nil || created.Data.Description != replacementDesc {
			t.Fatalf("replacement response = %#v", created.Data)
		}
		if got := countAddressRows(t, db, target); got != 1 {
			t.Fatalf("final target row count = %d, want 1", got)
		}
		var oldCount, newCount, globalCount int
		if err := db.QueryRow("SELECT COUNT(*) FILTER (WHERE id=$1), COUNT(*) FILTER (WHERE id=$2), COUNT(*) FROM ip_allocations", oldID, created.Data.ID).Scan(&oldCount, &newCount, &globalCount); err != nil {
			t.Fatalf("count final X3 commit rows: %v", err)
		}
		if oldCount != 0 || newCount != 1 || globalCount != 1 {
			t.Fatalf("final X3 commit counts = old:%d new:%d global:%d; want 0/1/1", oldCount, newCount, globalCount)
		}
		assertX3CommittedAllocation(t, db, created.Data.ID, subnet.ID, target, string(domain.AllocationStatusReserved), replacementDesc)
		finalSubnet, err := subnetService.GetSubnet(ctx, subnet.ID)
		if err != nil {
			t.Fatalf("read final X3 commit subnet: %v", err)
		}
		if finalSubnet.ReservedCount != 1 {
			t.Fatalf("final X3 commit ReservedCount = %d, want 1", finalSubnet.ReservedCount)
		}
	})

	t.Run("unreserve rollback preserves old row and rejects reserve", func(t *testing.T) {
		db := setupTestDB(t)
		ctx := context.Background()
		subnetRepo := postgres.NewSubnetRepository(db)
		subnetService := service.NewSubnetService(subnetRepo)
		allocationRepo := postgres.NewAllocationRepository(db)
		mux := setupUnreservationTestMux(subnetRepo, allocationRepo)
		subnet := createTestSubnet(t, subnetRepo, cidr, nil, "X3 rollback")
		oldID := insertAllocation(t, db, subnet.ID, target, domain.AllocationStatusReserved, nil, originalDesc)
		assertX3UniqueAddressConstraint(t, db)

		barrier := installX3AfterDeleteBarrier(t, db, oldID, true)
		unreserveDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			unreserveDone <- serveUnreservationRequest(mux, http.MethodDelete, fmt.Sprintf("/api/v1/ip-allocations/%d", oldID), "")
		}()
		unreservePID := waitForX3AdvisoryWaiterPID(t, db, barrier.holderPID)

		reserveDone := make(chan *httptest.ResponseRecorder, 1)
		body := fmt.Sprintf(`{"subnet_id":%d,"address":%q,"description":%q}`, subnet.ID, target, replacementDesc)
		go func() {
			reserveDone <- serveUnreservationRequest(mux, http.MethodPost, "/api/v1/ip-allocations", body)
		}()
		evidence := waitForX3InsertBlockedByPID(t, db, unreservePID)
		t.Logf("X3 rollback evidence: holderPID=%d unreservePID=%d reservePID=%d wait_event=%s ungranted_locks=%q blocking_pids=%q", barrier.holderPID, unreservePID, evidence.PID, evidence.WaitEvent, evidence.UngrantedLockTypes, evidence.BlockingPIDs)
		assertX3HTTPPending(t, "Unreserve", unreserveDone)
		assertX3HTTPPending(t, "Reserve", reserveDone)
		if got := countAddressRows(t, db, target); got != 1 {
			t.Fatalf("committed target row count while both transactions wait = %d, want 1", got)
		}
		assertX3CommittedOldAllocation(t, db, oldID, subnet.ID, target, originalDesc)

		barrier.release(t)
		unreserveResponse := receiveX3HTTPResponse(t, "Unreserve rollback response", unreserveDone)
		reserveResponse := receiveX3HTTPResponse(t, "Reserve duplicate response", reserveDone)
		assertX3ResponseError(t, unreserveResponse, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred.")
		assertX3ResponseError(t, reserveResponse, http.StatusConflict, "IP_ALREADY_ALLOCATED", "The IP address is already allocated.")
		if got := countAddressRows(t, db, target); got != 1 {
			t.Fatalf("final target row count = %d, want 1", got)
		}
		var oldCount, globalCount int
		if err := db.QueryRow("SELECT COUNT(*) FILTER (WHERE id=$1), COUNT(*) FROM ip_allocations", oldID).Scan(&oldCount, &globalCount); err != nil {
			t.Fatalf("count final X3 rollback rows: %v", err)
		}
		if oldCount != 1 || globalCount != 1 {
			t.Fatalf("final X3 rollback counts = old:%d global:%d; want 1/1", oldCount, globalCount)
		}
		assertX3CommittedOldAllocation(t, db, oldID, subnet.ID, target, originalDesc)
		finalSubnet, err := subnetService.GetSubnet(ctx, subnet.ID)
		if err != nil {
			t.Fatalf("read final X3 rollback subnet: %v", err)
		}
		if finalSubnet.ReservedCount != 1 {
			t.Fatalf("final X3 rollback ReservedCount = %d, want 1", finalSubnet.ReservedCount)
		}
	})
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
