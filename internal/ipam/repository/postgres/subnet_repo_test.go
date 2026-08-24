package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/lib/pq"
	"github.com/mowfteedev/mowf-net/internal/ipam/domain"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository/postgres"
)

func getFreePort(t *testing.T) uint32 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to acquire free TCP port: %v", err)
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port)
}

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	tempDir := t.TempDir()
	runtimePath := filepath.Join(tempDir, "runtime")
	dataPath := filepath.Join(tempDir, "data")
	port := getFreePort(t)

	config := embeddedpostgres.DefaultConfig().
		Port(port).
		RuntimePath(runtimePath).
		DataPath(dataPath).
		Database("mowf_net_test").
		Username("postgres").
		Password("postgres")

	pg := embeddedpostgres.NewDatabase(config)
	if err := pg.Start(); err != nil {
		t.Fatalf("failed to start isolated embedded postgres: %v", err)
	}

	t.Cleanup(func() {
		_ = pg.Stop()
	})

	connStr := fmt.Sprintf("host=127.0.0.1 port=%d user=postgres password=postgres dbname=mowf_net_test sslmode=disable", port)
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatalf("failed to open database connection: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Close()
	})

	if err := db.Ping(); err != nil {
		t.Fatalf("failed to ping isolated database: %v", err)
	}

	// Apply migrations
	migrationsDir := filepath.Join("..", "..", "..", "..", "migrations")
	vlanUp, err := os.ReadFile(filepath.Join(migrationsDir, "000001_create_vlans_table.up.sql"))
	if err != nil {
		t.Fatalf("failed to read vlan up migration: %v", err)
	}
	if _, err := db.Exec(string(vlanUp)); err != nil {
		t.Fatalf("failed to execute vlan up migration: %v", err)
	}

	subnetUp, err := os.ReadFile(filepath.Join(migrationsDir, "000002_create_subnets_table.up.sql"))
	if err != nil {
		t.Fatalf("failed to read subnet up migration: %v", err)
	}
	if _, err := db.Exec(string(subnetUp)); err != nil {
		t.Fatalf("failed to execute subnet up migration: %v", err)
	}

	return db
}

func countSubnets(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM subnets").Scan(&count)
	if err != nil {
		t.Fatalf("failed to count subnets: %v", err)
	}
	return count
}

func TestSubnetRepo_Create_Valid(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()

	validCIDRs := []string{
		"192.168.1.0/24",
		"10.0.0.0/8",
		"172.16.0.0/16",
		"192.168.2.0/30",
	}

	for _, cidrStr := range validCIDRs {
		cidr, err := domain.ParseCIDR(cidrStr)
		if err != nil {
			t.Fatalf("failed to parse CIDR %q: %v", cidrStr, err)
		}

		subnet := domain.NewSubnet(cidr, nil, "Test Subnet")
		if err := repo.Create(ctx, &subnet); err != nil {
			t.Fatalf("Create(%q) failed: %v", cidrStr, err)
		}

		if subnet.ID <= 0 {
			t.Errorf("expected positive ID for %q, got %d", cidrStr, subnet.ID)
		}
		if subnet.CreatedAt.IsZero() || subnet.UpdatedAt.IsZero() {
			t.Errorf("expected timestamps to be set for %q", cidrStr)
		}
	}
}

func TestSubnetRepo_Create_OverlapRejection(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()

	// 1. Insert baseline subnet
	baseCIDR, err := domain.ParseCIDR("192.168.1.0/24")
	if err != nil {
		t.Fatalf("failed to parse baseline CIDR: %v", err)
	}
	baseSubnet := domain.NewSubnet(baseCIDR, nil, "Base 192.168.1.0/24")
	if err := repo.Create(ctx, &baseSubnet); err != nil {
		t.Fatalf("failed to create baseline subnet: %v", err)
	}

	initialCount := countSubnets(t, db)

	// 2. Test overlap rejections
	overlapCases := []struct {
		name string
		cidr string
	}{
		{"candidate inside existing (lower half)", "192.168.1.0/25"},
		{"candidate inside existing (upper half)", "192.168.1.128/25"},
		{"candidate contains existing (supernet)", "192.168.0.0/23"},
		{"candidate exact duplicate", "192.168.1.0/24"},
		{"candidate inside existing (/30)", "192.168.1.0/30"},
		{"candidate inside existing (/30 offset)", "192.168.1.4/30"},
	}

	for _, tt := range overlapCases {
		t.Run(tt.name, func(t *testing.T) {
			cidr, err := domain.ParseCIDR(tt.cidr)
			if err != nil {
				t.Fatalf("failed to parse CIDR %q: %v", tt.cidr, err)
			}

			sub := domain.NewSubnet(cidr, nil, tt.name)
			err = repo.Create(ctx, &sub)
			if err == nil {
				t.Fatalf("expected ErrSubnetOverlap for %q, got nil", tt.cidr)
			}
			if !errors.Is(err, domain.ErrSubnetOverlap) {
				t.Errorf("expected ErrSubnetOverlap for %q, got %v", tt.cidr, err)
			}

			// Verify row was NOT inserted into DB
			currentCount := countSubnets(t, db)
			if currentCount != initialCount {
				t.Errorf("DB row count changed after rejected overlap! got %d, want %d", currentCount, initialCount)
			}
		})
	}

	// 3. Test non-overlapping adjacent subnets succeed
	adjacentCases := []struct {
		name string
		cidr string
	}{
		{"adjacent left", "192.168.0.0/24"},
		{"adjacent right", "192.168.2.0/24"},
	}

	for _, tt := range adjacentCases {
		t.Run(tt.name, func(t *testing.T) {
			cidr, err := domain.ParseCIDR(tt.cidr)
			if err != nil {
				t.Fatalf("failed to parse CIDR %q: %v", tt.cidr, err)
			}

			sub := domain.NewSubnet(cidr, nil, tt.name)
			if err := repo.Create(ctx, &sub); err != nil {
				t.Fatalf("adjacent subnet %q should succeed, got error: %v", tt.cidr, err)
			}
		})
	}
}

func TestSubnetRepo_Create_VlanRef(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()

	// 1. Invalid VLAN ref fails with ErrVlanNotFound
	cidrA, _ := domain.ParseCIDR("192.168.10.0/24")
	invalidVlanID := int64(99999)
	subA := domain.NewSubnet(cidrA, &invalidVlanID, "Subnet with non-existent VLAN")
	err := repo.Create(ctx, &subA)
	if err == nil {
		t.Fatalf("expected error for invalid vlan_ref_id, got nil")
	}
	if !errors.Is(err, domain.ErrVlanNotFound) {
		t.Errorf("expected ErrVlanNotFound, got %v", err)
	}

	// 2. Insert valid VLAN
	var vlanID int64
	err = db.QueryRow("INSERT INTO vlans (vlan_id, name, description) VALUES (100, 'Test VLAN', '') RETURNING id").Scan(&vlanID)
	if err != nil {
		t.Fatalf("failed to insert VLAN: %v", err)
	}

	// 3. Valid VLAN ref succeeds
	cidrB, _ := domain.ParseCIDR("192.168.10.0/24")
	subB := domain.NewSubnet(cidrB, &vlanID, "Subnet with valid VLAN")
	if err := repo.Create(ctx, &subB); err != nil {
		t.Fatalf("expected success with valid vlan_ref_id, got: %v", err)
	}
	if subB.VlanRefID == nil || *subB.VlanRefID != vlanID {
		t.Errorf("expected VlanRefID %d, got %v", vlanID, subB.VlanRefID)
	}
}

func TestSubnetRepo_Create_ConcurrentOutcome(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)

	// Concurrently attempt to create:
	// A = 10.10.0.0/24
	// B = 10.10.0.0/25 (subset of A)
	// Because A and B have different prefix lengths (/24 vs /25), UNIQUE(network, prefix_length)
	// would NOT stop them if both executed their SELECT EXISTS before either committed.
	// Only pg_advisory_xact_lock serializes them to guarantee exactly 1 winner.

	cidrA, err := domain.ParseCIDR("10.10.0.0/24")
	if err != nil {
		t.Fatalf("failed to parse CIDR A: %v", err)
	}
	cidrB, err := domain.ParseCIDR("10.10.0.0/25")
	if err != nil {
		t.Fatalf("failed to parse CIDR B: %v", err)
	}

	startBarrier := make(chan struct{})
	var wg sync.WaitGroup

	type result struct {
		name string
		err  error
	}
	results := make(chan result, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-startBarrier
		sub := domain.NewSubnet(cidrA, nil, "Concurrent A")
		err := repo.Create(context.Background(), &sub)
		results <- result{name: "A", err: err}
	}()

	go func() {
		defer wg.Done()
		<-startBarrier
		sub := domain.NewSubnet(cidrB, nil, "Concurrent B")
		err := repo.Create(context.Background(), &sub)
		results <- result{name: "B", err: err}
	}()

	// Release both goroutines at the exact same instant
	close(startBarrier)
	wg.Wait()
	close(results)

	var successCount, overlapCount int
	for res := range results {
		if res.err == nil {
			successCount++
			t.Logf("Candidate %s succeeded", res.name)
		} else if errors.Is(res.err, domain.ErrSubnetOverlap) {
			overlapCount++
			t.Logf("Candidate %s correctly rejected with ErrSubnetOverlap", res.name)
		} else {
			t.Errorf("Candidate %s returned unexpected error: %v", res.name, res.err)
		}
	}

	if successCount != 1 {
		t.Errorf("expected exactly 1 success, got %d", successCount)
	}
	if overlapCount != 1 {
		t.Errorf("expected exactly 1 ErrSubnetOverlap, got %d", overlapCount)
	}

	// Verify exactly 1 row exists in the 10.10.0.0/16 region
	var regionCount int
	err = db.QueryRow(`
		SELECT COUNT(*) FROM subnets
		WHERE set_masklen(network, prefix_length) && '10.10.0.0/16'::inet
	`).Scan(&regionCount)
	if err != nil {
		t.Fatalf("failed to query region count: %v", err)
	}
	if regionCount != 1 {
		t.Fatalf("expected exactly 1 row in DB region 10.10.0.0/16, got %d", regionCount)
	}
}

func TestSubnetRepo_Create_AdvisoryLockDeterministic(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()

	// Candidates:
	// A = 10.30.0.0/24
	// B = 10.30.0.0/25 (subset of A)
	// Different prefix lengths (/24 vs /25) ensure UNIQUE constraint on (network, prefix_length) cannot prevent overlap.
	cidrA, err := domain.ParseCIDR("10.30.0.0/24")
	if err != nil {
		t.Fatalf("failed to parse CIDR A: %v", err)
	}
	cidrB, err := domain.ParseCIDR("10.30.0.0/25")
	if err != nil {
		t.Fatalf("failed to parse CIDR B: %v", err)
	}

	// 1. Transaction A begins and explicitly acquires the shared SubnetCoordinationKey advisory lock
	txA, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin TxA: %v", err)
	}
	defer func() { _ = txA.Rollback() }()

	if _, err := txA.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", postgres.SubnetCoordinationKey); err != nil {
		t.Fatalf("TxA failed to acquire advisory lock: %v", err)
	}

	// 2. Start Transaction B via repo.Create on another connection, which must attempt the same advisory lock
	type bResult struct {
		err error
	}
	bDone := make(chan bResult, 1)

	go func() {
		subB := domain.NewSubnet(cidrB, nil, "Subnet B")
		bErr := repo.Create(context.Background(), &subB)
		bDone <- bResult{err: bErr}
	}()

	// 3. Poll PostgreSQL pg_locks to deterministically verify that Transaction B is observed WAITING (NOT granted)
	var waitingObserved bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		query := `
			SELECT COUNT(*) FROM pg_locks
			WHERE locktype = 'advisory'
			  AND NOT granted
		`
		err := db.QueryRow(query).Scan(&count)
		if err == nil && count > 0 {
			waitingObserved = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !waitingObserved {
		t.Fatalf("deterministic assertion failed: Transaction B was not observed waiting on advisory lock in pg_locks")
	}

	// 4. Verify Transaction B has not finished while TxA holds the lock
	select {
	case res := <-bDone:
		t.Fatalf("Transaction B completed prematurely before TxA committed! Result: %v", res.err)
	default:
		// Expected: B is still blocked
	}

	// 5. Transaction A inserts Subnet A and commits
	_, err = txA.ExecContext(ctx, `
		INSERT INTO subnets (network, prefix_length, description, created_at, updated_at)
		VALUES ($1::inet, $2, $3, NOW(), NOW())
	`, cidrA.Network(), cidrA.PrefixLength(), "Subnet A")
	if err != nil {
		t.Fatalf("TxA failed to insert subnet: %v", err)
	}

	if err := txA.Commit(); err != nil {
		t.Fatalf("TxA failed to commit: %v", err)
	}

	// 6. Now that TxA committed and released the lock, Transaction B unblocks,
	// acquires the advisory lock, executes overlap check against newly committed state, and returns ErrSubnetOverlap.
	var resB bResult
	select {
	case resB = <-bDone:
		// Completed
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for Transaction B to complete after TxA commit")
	}

	if resB.err == nil {
		t.Fatalf("Transaction B succeeded unexpectedly; wanted ErrSubnetOverlap")
	}
	if !errors.Is(resB.err, domain.ErrSubnetOverlap) {
		t.Fatalf("Transaction B returned unexpected error: %v, want ErrSubnetOverlap", resB.err)
	}

	// 7. Verify exactly 1 row exists in the 10.30.0.0/16 region
	var regionCount int
	err = db.QueryRow(`
		SELECT COUNT(*) FROM subnets
		WHERE set_masklen(network, prefix_length) && '10.30.0.0/16'::inet
	`).Scan(&regionCount)
	if err != nil {
		t.Fatalf("failed to query region count: %v", err)
	}
	if regionCount != 1 {
		t.Fatalf("expected exactly 1 row in DB region 10.30.0.0/16, got %d", regionCount)
	}
}

func TestSubnetRepo_Create_Stress(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)

	// 10 goroutines racing to insert mutually overlapping subnets all containing 10.20.0.0
	candidates := []string{
		"10.20.0.0/21",
		"10.20.0.0/22",
		"10.20.0.0/23",
		"10.20.0.0/24",
		"10.20.0.0/25",
		"10.20.0.0/26",
		"10.20.0.0/27",
		"10.20.0.0/28",
		"10.20.0.0/29",
		"10.20.0.0/30",
	}

	n := len(candidates)
	startBarrier := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, n)

	for _, cidrStr := range candidates {
		wg.Add(1)
		go func(cStr string) {
			defer wg.Done()
			cidr, _ := domain.ParseCIDR(cStr)
			sub := domain.NewSubnet(cidr, nil, "Stress "+cStr)

			<-startBarrier
			err := repo.Create(context.Background(), &sub)
			errCh <- err
		}(cidrStr)
	}

	close(startBarrier)
	wg.Wait()
	close(errCh)

	var successCount, overlapCount, otherCount int
	for err := range errCh {
		if err == nil {
			successCount++
		} else if errors.Is(err, domain.ErrSubnetOverlap) {
			overlapCount++
		} else {
			otherCount++
			t.Errorf("unexpected error in stress test: %v", err)
		}
	}

	if successCount != 1 {
		t.Errorf("expected exactly 1 success among 10 overlapping candidates, got %d", successCount)
	}
	if overlapCount != n-1 {
		t.Errorf("expected %d overlaps, got %d", n-1, overlapCount)
	}
	if otherCount != 0 {
		t.Errorf("expected 0 unexpected errors, got %d", otherCount)
	}

	// Verify exactly 1 row in the 10.20.0.0/16 region
	var regionCount int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM subnets
		WHERE set_masklen(network, prefix_length) && '10.20.0.0/16'::inet
	`).Scan(&regionCount)
	if err != nil {
		t.Fatalf("failed to query region count: %v", err)
	}
	if regionCount != 1 {
		t.Fatalf("expected exactly 1 row in DB region 10.20.0.0/16, got %d", regionCount)
	}
}

func TestSubnetRepo_GetByID(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()

	// 1. Insert a VLAN
	var vlanID int64
	err := db.QueryRow("INSERT INTO vlans (vlan_id, name, description) VALUES (10, 'VLAN 10', '') RETURNING id").Scan(&vlanID)
	if err != nil {
		t.Fatalf("failed to insert test VLAN: %v", err)
	}

	// 2. Insert representative non-overlapping CIDRs including boundary /1 and /30
	testCases := []struct {
		cidr        string
		vlanRefID   *int64
		description string
	}{
		{"0.0.0.0/1", nil, "Boundary /1 subnet (lower half)"},
		{"128.0.0.0/8", &vlanID, "Class A /8 subnet (upper half)"},
		{"172.16.0.0/16", nil, "Class B /16 subnet"},
		{"192.168.1.0/24", &vlanID, "Class C /24 subnet"},
		{"192.168.2.0/30", nil, "Point-to-point /30 subnet"},
	}

	for _, tc := range testCases {
		cidr, err := domain.ParseCIDR(tc.cidr)
		if err != nil {
			t.Fatalf("failed to parse CIDR %q: %v", tc.cidr, err)
		}

		sub := domain.NewSubnet(cidr, tc.vlanRefID, tc.description)
		if err := repo.Create(ctx, &sub); err != nil {
			t.Fatalf("Create(%q) failed: %v", tc.cidr, err)
		}

		// Retrieve by ID
		fetched, err := repo.GetByID(ctx, sub.ID)
		if err != nil {
			t.Fatalf("GetByID(%d) for %q failed: %v", sub.ID, tc.cidr, err)
		}

		// Verify reconstructed canonical CIDR
		if fetched.CIDR.CIDR() != tc.cidr {
			t.Errorf("GetByID CIDR = %s, want %s", fetched.CIDR.CIDR(), tc.cidr)
		}
		if fetched.CIDR.Network() != cidr.Network() {
			t.Errorf("GetByID Network = %s, want %s", fetched.CIDR.Network(), cidr.Network())
		}
		if fetched.CIDR.Broadcast() != cidr.Broadcast() {
			t.Errorf("GetByID Broadcast = %s, want %s", fetched.CIDR.Broadcast(), cidr.Broadcast())
		}
		if fetched.CIDR.FirstUsable() != cidr.FirstUsable() {
			t.Errorf("GetByID FirstUsable = %s, want %s", fetched.CIDR.FirstUsable(), cidr.FirstUsable())
		}
		if fetched.CIDR.LastUsable() != cidr.LastUsable() {
			t.Errorf("GetByID LastUsable = %s, want %s", fetched.CIDR.LastUsable(), cidr.LastUsable())
		}
		if fetched.CIDR.UsableCount() != cidr.UsableCount() {
			t.Errorf("GetByID UsableCount = %d, want %d", fetched.CIDR.UsableCount(), cidr.UsableCount())
		}
		if fetched.Description != tc.description {
			t.Errorf("GetByID Description = %q, want %q", fetched.Description, tc.description)
		}
		if tc.vlanRefID == nil && fetched.VlanRefID != nil {
			t.Errorf("GetByID VlanRefID = %v, want nil", fetched.VlanRefID)
		}
		if tc.vlanRefID != nil && (fetched.VlanRefID == nil || *fetched.VlanRefID != *tc.vlanRefID) {
			t.Errorf("GetByID VlanRefID = %v, want %v", fetched.VlanRefID, tc.vlanRefID)
		}
		if fetched.CreatedAt.IsZero() || fetched.UpdatedAt.IsZero() {
			t.Errorf("GetByID timestamps are zero: created=%v, updated=%v", fetched.CreatedAt, fetched.UpdatedAt)
		}
	}

	// 3. Test missing ID returns ErrSubnetNotFound
	_, err = repo.GetByID(ctx, 999999)
	if err == nil {
		t.Fatalf("expected error for non-existent ID, got nil")
	}
	if !errors.Is(err, domain.ErrSubnetNotFound) {
		t.Errorf("expected ErrSubnetNotFound, got %v", err)
	}
}

func TestSubnetRepo_List_PaginationAndFilters(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()

	// 1. Empty table list
	emptyList, nextCursor, err := repo.List(ctx, postgres.ListFilter{})
	if err != nil {
		t.Fatalf("List on empty table failed: %v", err)
	}
	if len(emptyList) != 0 {
		t.Errorf("len(emptyList) = %d, want 0", len(emptyList))
	}
	if nextCursor != nil {
		t.Errorf("nextCursor = %v, want nil", nextCursor)
	}

	// 2. Insert test VLANs
	var vlan10, vlan20 int64
	err = db.QueryRow("INSERT INTO vlans (vlan_id, name) VALUES (10, 'VLAN 10') RETURNING id").Scan(&vlan10)
	if err != nil {
		t.Fatalf("failed to insert VLAN 10: %v", err)
	}
	err = db.QueryRow("INSERT INTO vlans (vlan_id, name) VALUES (20, 'VLAN 20') RETURNING id").Scan(&vlan20)
	if err != nil {
		t.Fatalf("failed to insert VLAN 20: %v", err)
	}

	// 3. Insert 5 distinct subnets
	subnetsToCreate := []struct {
		cidr string
		vlan *int64
		desc string
	}{
		{"10.1.0.0/24", &vlan10, "Branch Office 1"},
		{"10.2.0.0/24", &vlan10, "Branch Office 2"},
		{"192.168.10.0/24", &vlan20, "Data Center LAN"},
		{"192.168.20.0/24", &vlan20, "Data Center DMZ"},
		{"172.16.0.0/16", nil, "Core Network"},
	}

	for _, s := range subnetsToCreate {
		cidr, _ := domain.ParseCIDR(s.cidr)
		sub := domain.NewSubnet(cidr, s.vlan, s.desc)
		if err := repo.Create(ctx, &sub); err != nil {
			t.Fatalf("Create(%q) failed: %v", s.cidr, err)
		}
	}

	// 4. Test default list returns all 5 in deterministic order (id ASC)
	allSubnets, next, err := repo.List(ctx, postgres.ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(allSubnets) != 5 {
		t.Fatalf("len(allSubnets) = %d, want 5", len(allSubnets))
	}
	if next != nil {
		t.Errorf("next cursor for full list should be nil, got %v", next)
	}
	for i := 1; i < len(allSubnets); i++ {
		if allSubnets[i].ID <= allSubnets[i-1].ID {
			t.Errorf("subnets are not in deterministic ascending ID order: id[%d]=%d <= id[%d]=%d",
				i, allSubnets[i].ID, i-1, allSubnets[i-1].ID)
		}
	}

	// 5. Test pagination with limit = 2
	// Page 1
	p1, c1, err := repo.List(ctx, postgres.ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("Page 1 failed: %v", err)
	}
	if len(p1) != 2 {
		t.Fatalf("len(p1) = %d, want 2", len(p1))
	}
	if c1 == nil || *c1 != p1[1].ID {
		t.Fatalf("c1 = %v, want %d", c1, p1[1].ID)
	}

	// Page 2
	p2, c2, err := repo.List(ctx, postgres.ListFilter{Limit: 2, Cursor: c1})
	if err != nil {
		t.Fatalf("Page 2 failed: %v", err)
	}
	if len(p2) != 2 {
		t.Fatalf("len(p2) = %d, want 2", len(p2))
	}
	if c2 == nil || *c2 != p2[1].ID {
		t.Fatalf("c2 = %v, want %d", c2, p2[1].ID)
	}

	// Page 3
	p3, c3, err := repo.List(ctx, postgres.ListFilter{Limit: 2, Cursor: c2})
	if err != nil {
		t.Fatalf("Page 3 failed: %v", err)
	}
	if len(p3) != 1 {
		t.Fatalf("len(p3) = %d, want 1", len(p3))
	}
	if c3 != nil {
		t.Fatalf("expected c3 == nil at end of list, got %v", c3)
	}

	// Verify no duplicates across pages
	seenIDs := make(map[int64]bool)
	allPages := append(append(p1, p2...), p3...)
	for _, sub := range allPages {
		if seenIDs[sub.ID] {
			t.Errorf("duplicate subnet ID %d across pagination pages", sub.ID)
		}
		seenIDs[sub.ID] = true
	}
	if len(seenIDs) != 5 {
		t.Errorf("total unique subnets retrieved = %d, want 5", len(seenIDs))
	}

	// 6. Test filter by vlan_ref_id
	v10List, _, err := repo.List(ctx, postgres.ListFilter{VlanRefID: &vlan10})
	if err != nil {
		t.Fatalf("List vlan 10 failed: %v", err)
	}
	if len(v10List) != 2 {
		t.Fatalf("len(v10List) = %d, want 2", len(v10List))
	}
	for _, sub := range v10List {
		if sub.VlanRefID == nil || *sub.VlanRefID != vlan10 {
			t.Errorf("subnet %d has vlan %v, want %d", sub.ID, sub.VlanRefID, vlan10)
		}
	}

	nonExistentVLAN := int64(99999)
	vEmptyList, _, err := repo.List(ctx, postgres.ListFilter{VlanRefID: &nonExistentVLAN})
	if err != nil {
		t.Fatalf("List non-existent vlan failed: %v", err)
	}
	if len(vEmptyList) != 0 {
		t.Errorf("len(vEmptyList) = %d, want 0", len(vEmptyList))
	}

	// 7. Test search filter
	// 7a. Search by IP prefix
	searchIP, _, err := repo.List(ctx, postgres.ListFilter{Search: "192.168"})
	if err != nil {
		t.Fatalf("Search '192.168' failed: %v", err)
	}
	if len(searchIP) != 2 {
		t.Fatalf("len(searchIP) = %d, want 2", len(searchIP))
	}

	// 7b. Search by exact CIDR
	searchCIDR, _, err := repo.List(ctx, postgres.ListFilter{Search: "192.168.10.0/24"})
	if err != nil {
		t.Fatalf("Search '192.168.10.0/24' failed: %v", err)
	}
	if len(searchCIDR) != 1 || searchCIDR[0].CIDR.CIDR() != "192.168.10.0/24" {
		t.Fatalf("Search exact CIDR failed, got %v", searchCIDR)
	}

	// 7c. Search by description
	searchDesc, _, err := repo.List(ctx, postgres.ListFilter{Search: "Branch"})
	if err != nil {
		t.Fatalf("Search 'Branch' failed: %v", err)
	}
	if len(searchDesc) != 2 {
		t.Fatalf("len(searchDesc) = %d, want 2", len(searchDesc))
	}

	// 7d. Search no match
	searchNoMatch, _, err := repo.List(ctx, postgres.ListFilter{Search: "xyz_not_found"})
	if err != nil {
		t.Fatalf("Search no match failed: %v", err)
	}
	if len(searchNoMatch) != 0 {
		t.Errorf("len(searchNoMatch) = %d, want 0", len(searchNoMatch))
	}
}
