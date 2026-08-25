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
	"github.com/mowfteedev/mowf-net/internal/ipam/repository"
	"github.com/mowfteedev/mowf-net/internal/ipam/repository/postgres"
	"github.com/mowfteedev/mowf-net/internal/ipam/service"
)

func acquireFreePort() (uint32, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port), nil
}

var sharedTestDB *sql.DB

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "mowf-net-repository-postgres-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create PostgreSQL test directory: %v\n", err)
		os.Exit(1)
	}
	runtimePath := filepath.Join(tempDir, "runtime")
	dataPath := filepath.Join(tempDir, "data")
	port, err := acquireFreePort()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to acquire PostgreSQL test port: %v\n", err)
		os.Exit(1)
	}

	config := embeddedpostgres.DefaultConfig().
		Port(port).
		RuntimePath(runtimePath).
		DataPath(dataPath).
		Database("mowf_net_test").
		Username("postgres").
		Password("postgres")

	pg := embeddedpostgres.NewDatabase(config)
	if err := pg.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start isolated embedded PostgreSQL: %v\n", err)
		os.Exit(1)
	}

	connStr := fmt.Sprintf("host=127.0.0.1 port=%d user=postgres password=postgres dbname=mowf_net_test sslmode=disable", port)
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open PostgreSQL test connection: %v\n", err)
		_ = pg.Stop()
		os.Exit(1)
	}
	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to ping isolated PostgreSQL: %v\n", err)
		_ = db.Close()
		_ = pg.Stop()
		os.Exit(1)
	}

	migrationsDir := filepath.Join("..", "..", "..", "..", "migrations")
	for _, filename := range []string{
		"000001_create_vlans_table.up.sql",
		"000002_create_subnets_table.up.sql",
		"000003_create_ip_allocations_table.up.sql",
	} {
		migration, readErr := os.ReadFile(filepath.Join(migrationsDir, filename))
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "failed to read %s: %v\n", filename, readErr)
			_ = db.Close()
			_ = pg.Stop()
			os.Exit(1)
		}
		if _, execErr := db.Exec(string(migration)); execErr != nil {
			fmt.Fprintf(os.Stderr, "failed to execute %s: %v\n", filename, execErr)
			_ = db.Close()
			_ = pg.Stop()
			os.Exit(1)
		}
	}

	sharedTestDB = db
	code := m.Run()
	_ = db.Close()
	_ = pg.Stop()
	_ = os.RemoveAll(tempDir)
	os.Exit(code)
}

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	if sharedTestDB == nil {
		t.Fatal("shared PostgreSQL test database was not initialized")
	}
	resetTestData(t, sharedTestDB)
	return sharedTestDB
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

func resetTestData(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec("TRUNCATE TABLE ip_allocations, subnets, vlans RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("failed to reset isolated test data: %v", err)
	}
}

func createTestSubnet(t *testing.T, repo *postgres.SubnetRepository, cidrString string, vlanRefID *int64, description string) domain.Subnet {
	t.Helper()
	cidr, err := domain.ParseCIDR(cidrString)
	if err != nil {
		t.Fatalf("failed to parse test CIDR %q: %v", cidrString, err)
	}
	subnet := domain.NewSubnet(cidr, vlanRefID, description)
	if err := repo.Create(context.Background(), &subnet); err != nil {
		t.Fatalf("failed to create test subnet %q: %v", cidrString, err)
	}
	return subnet
}

func insertTestAllocation(t *testing.T, db *sql.DB, subnetID int64, address, status string) int64 {
	t.Helper()
	var interfaceID any
	if status == "assigned" {
		// M1-B3 has no interface FK by design; a non-null test-only BIGINT is valid.
		interfaceID = int64(1)
	}
	var id int64
	if err := db.QueryRow(`
		INSERT INTO ip_allocations (subnet_id, address, status, interface_id)
		VALUES ($1, $2::inet, $3, $4) RETURNING id
	`, subnetID, address, status, interfaceID).Scan(&id); err != nil {
		t.Fatalf("failed to insert %s allocation %s: %v", status, address, err)
	}
	return id
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

func TestSubnetRepo_AllocationCounts_GetAndBoundedList(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()

	var vlanID int64
	if err := db.QueryRow("INSERT INTO vlans (vlan_id, name) VALUES (30, 'Count VLAN') RETURNING id").Scan(&vlanID); err != nil {
		t.Fatalf("failed to insert count VLAN: %v", err)
	}
	noAlloc := createTestSubnet(t, repo, "10.70.0.0/24", &vlanID, "count page zero")
	mixed := createTestSubnet(t, repo, "10.70.1.0/24", &vlanID, "count page mixed")
	assignedOnly := createTestSubnet(t, repo, "10.70.2.0/24", nil, "count page assigned")
	reservedOnly := createTestSubnet(t, repo, "10.70.3.0/24", &vlanID, "other reserved")

	insertTestAllocation(t, db, assignedOnly.ID, "10.70.2.10", "assigned")
	insertTestAllocation(t, db, reservedOnly.ID, "10.70.3.10", "reserved")
	for _, address := range []string{"10.70.1.10", "10.70.1.11"} {
		insertTestAllocation(t, db, mixed.ID, address, "assigned")
	}
	for _, address := range []string{"10.70.1.20", "10.70.1.21", "10.70.1.22"} {
		insertTestAllocation(t, db, mixed.ID, address, "reserved")
	}

	zeroRead, err := repo.GetByID(ctx, noAlloc.ID)
	if err != nil {
		t.Fatalf("GetByID(no allocations) failed: %v", err)
	}
	if zeroRead.AssignedCount != 0 || zeroRead.ReservedCount != 0 {
		t.Fatalf("no-allocation counts = %d/%d, want 0/0", zeroRead.AssignedCount, zeroRead.ReservedCount)
	}
	mixedRead, err := repo.GetByID(ctx, mixed.ID)
	if err != nil {
		t.Fatalf("GetByID(mixed) failed: %v", err)
	}
	if mixedRead.AssignedCount != 2 || mixedRead.ReservedCount != 3 {
		t.Fatalf("mixed counts = %d/%d, want 2/3", mixedRead.AssignedCount, mixedRead.ReservedCount)
	}

	// Combined keyset pagination + VLAN + search proves that the bounded page
	// remains correct after the aggregate join. The list implementation executes
	// this as one SQL query, not one count query per Subnet.
	page1, cursor, err := repo.List(ctx, postgres.ListFilter{VlanRefID: &vlanID, Search: "count page", Limit: 1})
	if err != nil {
		t.Fatalf("combined count page 1 failed: %v", err)
	}
	if len(page1) != 1 || page1[0].ID != noAlloc.ID || page1[0].AssignedCount != 0 || page1[0].ReservedCount != 0 {
		t.Fatalf("unexpected combined page 1: %+v", page1)
	}
	if cursor == nil {
		t.Fatal("combined count page 1 missing cursor")
	}
	page2, cursor2, err := repo.List(ctx, postgres.ListFilter{VlanRefID: &vlanID, Search: "count page", Limit: 1, Cursor: cursor})
	if err != nil {
		t.Fatalf("combined count page 2 failed: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != mixed.ID || page2[0].AssignedCount != 2 || page2[0].ReservedCount != 3 {
		t.Fatalf("unexpected combined page 2: %+v", page2)
	}
	if cursor2 != nil {
		t.Fatalf("combined count page 2 cursor = %v, want nil", cursor2)
	}

	all, _, err := repo.List(ctx, postgres.ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("multi-subnet count list failed: %v", err)
	}
	want := map[int64][2]int64{
		noAlloc.ID: {0, 0}, mixed.ID: {2, 3}, assignedOnly.ID: {1, 0}, reservedOnly.ID: {0, 1},
	}
	for _, read := range all {
		counts := want[read.ID]
		if read.AssignedCount != counts[0] || read.ReservedCount != counts[1] {
			t.Errorf("subnet %d counts = %d/%d, want %d/%d", read.ID, read.AssignedCount, read.ReservedCount, counts[0], counts[1])
		}
	}
}

func TestSubnetRepo_ResizeBoundariesAndSafety(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()

	type allocationSnapshot struct {
		ID          int64
		Address     string
		Status      string
		InterfaceID sql.NullInt64
		Description string
		CreatedAt   time.Time
		UpdatedAt   time.Time
	}
	snapshot := func(t *testing.T, subnetID int64) []allocationSnapshot {
		t.Helper()
		rows, err := db.Query(`SELECT id, host(address), status, interface_id, description, created_at, updated_at FROM ip_allocations WHERE subnet_id=$1 ORDER BY id`, subnetID)
		if err != nil {
			t.Fatalf("failed to snapshot allocations: %v", err)
		}
		defer rows.Close()
		var result []allocationSnapshot
		for rows.Next() {
			var item allocationSnapshot
			if err := rows.Scan(&item.ID, &item.Address, &item.Status, &item.InterfaceID, &item.Description, &item.CreatedAt, &item.UpdatedAt); err != nil {
				t.Fatalf("failed to scan allocation snapshot: %v", err)
			}
			result = append(result, item)
		}
		return result
	}
	resize := func(t *testing.T, id int64, cidrString string) error {
		t.Helper()
		_, err := repo.Update(ctx, id, repository.UpdateSubnet{CIDR: cidrString, CIDRSet: true})
		return err
	}

	conflicts := []struct {
		name      string
		address   string
		status    string
		candidate string
	}{
		{"C1_reserved_outside", "10.80.0.200", "reserved", "10.80.0.0/25"},
		{"C2_assigned_outside", "10.80.0.200", "assigned", "10.80.0.0/25"},
		{"C3_candidate_network", "10.80.0.128", "reserved", "10.80.0.128/25"},
		{"C4_candidate_broadcast", "10.80.0.127", "assigned", "10.80.0.0/25"},
	}
	for _, tc := range conflicts {
		t.Run(tc.name, func(t *testing.T) {
			resetTestData(t, db)
			subnet := createTestSubnet(t, repo, "10.80.0.0/24", nil, tc.name)
			if tc.name == "C3_candidate_network" {
				usable, err := subnet.CIDR.IsUsableIPString(tc.address)
				if err != nil || !usable {
					t.Fatalf("C3 allocation %s must be usable in original %s: usable=%v err=%v", tc.address, subnet.CIDR.CIDR(), usable, err)
				}
				candidate, err := domain.ParseCIDR(tc.candidate)
				if err != nil {
					t.Fatalf("failed to parse C3 candidate: %v", err)
				}
				if candidate.Network() != tc.address {
					t.Fatalf("C3 allocation %s must equal candidate network %s", tc.address, candidate.Network())
				}
			}
			insertTestAllocation(t, db, subnet.ID, tc.address, tc.status)
			before := snapshot(t, subnet.ID)
			err := resize(t, subnet.ID, tc.candidate)
			if !errors.Is(err, domain.ErrSubnetResizeConflict) {
				t.Fatalf("resize error = %v, want ErrSubnetResizeConflict", err)
			}
			after := snapshot(t, subnet.ID)
			if fmt.Sprint(after) != fmt.Sprint(before) {
				t.Fatalf("failed resize changed allocation: before=%+v after=%+v", before, after)
			}
		})
	}

	t.Run("C5_first_and_last_usable_safe_shrink", func(t *testing.T) {
		resetTestData(t, db)
		subnet := createTestSubnet(t, repo, "10.80.0.0/24", nil, "boundaries")
		insertTestAllocation(t, db, subnet.ID, "10.80.0.1", "reserved")
		insertTestAllocation(t, db, subnet.ID, "10.80.0.126", "assigned")
		before := snapshot(t, subnet.ID)
		if err := resize(t, subnet.ID, "10.80.0.0/25"); err != nil {
			t.Fatalf("safe boundary shrink failed: %v", err)
		}
		after := snapshot(t, subnet.ID)
		if fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatalf("safe resize changed allocation: before=%+v after=%+v", before, after)
		}
	})

	t.Run("safe_grow", func(t *testing.T) {
		resetTestData(t, db)
		subnet := createTestSubnet(t, repo, "10.80.0.0/25", nil, "grow")
		insertTestAllocation(t, db, subnet.ID, "10.80.0.126", "reserved")
		if err := resize(t, subnet.ID, "10.80.0.0/24"); err != nil {
			t.Fatalf("safe grow failed: %v", err)
		}
	})

	t.Run("overlap_excludes_target_but_checks_other_subnets", func(t *testing.T) {
		resetTestData(t, db)
		target := createTestSubnet(t, repo, "10.80.0.0/25", nil, "target")
		_ = createTestSubnet(t, repo, "10.80.0.128/25", nil, "other")
		if err := resize(t, target.ID, "10.80.0.0/24"); !errors.Is(err, domain.ErrSubnetOverlap) {
			t.Fatalf("overlap resize error = %v, want ErrSubnetOverlap", err)
		}
	})
}

func TestSubnetRepo_UpdateFieldsAndDelete(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()

	var vlanID int64
	if err := db.QueryRow("INSERT INTO vlans (vlan_id, name) VALUES (81, 'Patch VLAN') RETURNING id").Scan(&vlanID); err != nil {
		t.Fatalf("failed to insert patch VLAN: %v", err)
	}
	subnet := createTestSubnet(t, repo, "10.81.0.0/24", nil, "old")
	read, err := repo.Update(ctx, subnet.ID, repository.UpdateSubnet{
		VlanRefID: &vlanID, VlanRefIDSet: true, Description: "new", DescriptionSet: true,
	})
	if err != nil {
		t.Fatalf("non-CIDR update failed: %v", err)
	}
	if read.Description != "new" || read.VlanRefID == nil || *read.VlanRefID != vlanID || read.CIDR.CIDR() != "10.81.0.0/24" {
		t.Fatalf("unexpected non-CIDR update: %+v", read)
	}
	read, err = repo.Update(ctx, subnet.ID, repository.UpdateSubnet{VlanRefIDSet: true})
	if err != nil || read.VlanRefID != nil {
		t.Fatalf("VLAN unlink result=%+v error=%v", read, err)
	}
	missingVLAN := int64(999999)
	if _, err := repo.Update(ctx, subnet.ID, repository.UpdateSubnet{VlanRefID: &missingVLAN, VlanRefIDSet: true}); !errors.Is(err, domain.ErrVlanNotFound) {
		t.Fatalf("missing VLAN update error = %v, want ErrVlanNotFound", err)
	}
	if _, err := repo.Update(ctx, 999999, repository.UpdateSubnet{Description: "x", DescriptionSet: true}); !errors.Is(err, domain.ErrSubnetNotFound) {
		t.Fatalf("missing Subnet update error = %v, want ErrSubnetNotFound", err)
	}

	for _, status := range []string{"reserved", "assigned"} {
		t.Run("delete_blocked_"+status, func(t *testing.T) {
			resetTestData(t, db)
			sub := createTestSubnet(t, repo, "10.82.0.0/24", nil, status)
			allocationID := insertTestAllocation(t, db, sub.ID, "10.82.0.10", status)
			if err := repo.Delete(ctx, sub.ID); !errors.Is(err, domain.ErrSubnetHasAllocations) {
				t.Fatalf("delete error = %v, want ErrSubnetHasAllocations", err)
			}
			var remaining int
			if err := db.QueryRow("SELECT COUNT(*) FROM ip_allocations WHERE id=$1 AND subnet_id=$2", allocationID, sub.ID).Scan(&remaining); err != nil || remaining != 1 {
				t.Fatalf("allocation did not remain after rejected delete: count=%d err=%v", remaining, err)
			}
		})
	}

	t.Run("delete_no_allocations", func(t *testing.T) {
		resetTestData(t, db)
		sub := createTestSubnet(t, repo, "10.82.0.0/24", nil, "delete")
		if err := repo.Delete(ctx, sub.ID); err != nil {
			t.Fatalf("delete without allocations failed: %v", err)
		}
		if _, err := repo.GetByID(ctx, sub.ID); !errors.Is(err, domain.ErrSubnetNotFound) {
			t.Fatalf("deleted subnet still exists: %v", err)
		}
		if err := repo.Delete(ctx, sub.ID); !errors.Is(err, domain.ErrSubnetNotFound) {
			t.Fatalf("missing delete error = %v, want ErrSubnetNotFound", err)
		}
	})

	t.Run("delete_does_not_use_global_advisory", func(t *testing.T) {
		resetTestData(t, db)
		sub := createTestSubnet(t, repo, "10.82.0.0/24", nil, "delete without advisory")
		blocker, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("failed to begin advisory blocker: %v", err)
		}
		if _, err := blocker.Exec("SELECT pg_advisory_xact_lock($1)", postgres.SubnetCoordinationKey); err != nil {
			t.Fatalf("failed to hold global advisory key: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- repo.Delete(context.Background(), sub.ID) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Delete failed while unrelated advisory key was held: %v", err)
			}
		case <-time.After(2 * time.Second):
			_ = blocker.Rollback()
			<-done
			t.Fatal("Delete waited on the global Subnet advisory key")
		}
		if err := blocker.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Fatalf("failed to release advisory blocker: %v", err)
		}
	})
}

func waitForPostgresCondition(t *testing.T, description string, condition func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok, err := condition()
		if err != nil {
			t.Fatalf("failed while observing %s: %v", description, err)
		}
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out observing %s", description)
}

func advisoryLockCount(db *sql.DB, granted bool) (int, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM pg_locks WHERE locktype='advisory' AND granted=$1", granted).Scan(&count)
	return count, err
}

func assertNoCommittedOverlap(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM subnets a JOIN subnets b ON a.id < b.id
		WHERE set_masklen(a.network, a.prefix_length) && set_masklen(b.network, b.prefix_length)
	`).Scan(&count); err != nil {
		t.Fatalf("failed to inspect committed overlap: %v", err)
	}
	if count != 0 {
		t.Fatalf("found %d overlapping committed subnet pairs", count)
	}
}

func TestSubnetRepo_Concurrency_CreateVsResize_AdvisoryWaitAndRecheck(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()
	target := createTestSubnet(t, repo, "10.90.0.0/26", nil, "resize target")

	// Hold the target row so production Resize can acquire the global advisory
	// lock and then deterministically pause at its required FOR UPDATE.
	rowBlocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to start row blocker: %v", err)
	}
	defer func() { _ = rowBlocker.Rollback() }()
	if _, err := rowBlocker.Exec("SELECT id FROM subnets WHERE id=$1 FOR UPDATE", target.ID); err != nil {
		t.Fatalf("failed to lock resize target: %v", err)
	}

	resizeCIDR, _ := domain.ParseCIDR("10.90.0.0/25")
	resizeDone := make(chan error, 1)
	go func() {
		_, err := repo.Update(context.Background(), target.ID, repository.UpdateSubnet{CIDR: resizeCIDR.CIDR(), CIDRSet: true})
		resizeDone <- err
	}()
	waitForPostgresCondition(t, "Resize holding the shared advisory key", func() (bool, error) {
		count, err := advisoryLockCount(db, true)
		return count > 0, err
	})

	createDone := make(chan error, 1)
	go func() {
		candidateCIDR, _ := domain.ParseCIDR("10.90.0.64/26")
		candidate := domain.NewSubnet(candidateCIDR, nil, "create competitor")
		createDone <- repo.Create(context.Background(), &candidate)
	}()
	waitForPostgresCondition(t, "Create waiting on Resize advisory key", func() (bool, error) {
		count, err := advisoryLockCount(db, false)
		return count > 0, err
	})
	select {
	case err := <-createDone:
		t.Fatalf("Create completed before advisory holder release: %v", err)
	default:
	}

	if err := rowBlocker.Commit(); err != nil {
		t.Fatalf("failed to release resize row blocker: %v", err)
	}
	if err := <-resizeDone; err != nil {
		t.Fatalf("Resize failed: %v", err)
	}
	if err := <-createDone; !errors.Is(err, domain.ErrSubnetOverlap) {
		t.Fatalf("serialized Create error = %v, want ErrSubnetOverlap", err)
	}
	assertNoCommittedOverlap(t, db)
}

func TestSubnetRepo_Concurrency_ResizeVsResize_AdvisoryWaitAndRecheck(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()
	targetA := createTestSubnet(t, repo, "10.91.0.0/26", nil, "resize A")
	targetB := createTestSubnet(t, repo, "10.91.0.128/26", nil, "resize B")

	rowBlocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to start row blocker: %v", err)
	}
	defer func() { _ = rowBlocker.Rollback() }()
	if _, err := rowBlocker.Exec("SELECT id FROM subnets WHERE id=$1 FOR UPDATE", targetA.ID); err != nil {
		t.Fatalf("failed to lock resize A: %v", err)
	}

	cidrA, _ := domain.ParseCIDR("10.91.0.0/25")
	doneA := make(chan error, 1)
	go func() {
		_, err := repo.Update(context.Background(), targetA.ID, repository.UpdateSubnet{CIDR: cidrA.CIDR(), CIDRSet: true})
		doneA <- err
	}()
	waitForPostgresCondition(t, "Resize A holding advisory key", func() (bool, error) {
		count, err := advisoryLockCount(db, true)
		return count > 0, err
	})

	cidrB, _ := domain.ParseCIDR("10.91.0.64/26")
	doneB := make(chan error, 1)
	go func() {
		_, err := repo.Update(context.Background(), targetB.ID, repository.UpdateSubnet{CIDR: cidrB.CIDR(), CIDRSet: true})
		doneB <- err
	}()
	waitForPostgresCondition(t, "Resize B waiting on Resize A advisory key", func() (bool, error) {
		count, err := advisoryLockCount(db, false)
		return count > 0, err
	})
	select {
	case err := <-doneB:
		t.Fatalf("Resize B completed before advisory release: %v", err)
	default:
	}

	if err := rowBlocker.Commit(); err != nil {
		t.Fatalf("failed to release resize A blocker: %v", err)
	}
	if err := <-doneA; err != nil {
		t.Fatalf("Resize A failed: %v", err)
	}
	if err := <-doneB; !errors.Is(err, domain.ErrSubnetOverlap) {
		t.Fatalf("serialized Resize B error = %v, want ErrSubnetOverlap", err)
	}
	assertNoCommittedOverlap(t, db)
}

func TestSubnetRepo_ResizeSameCIDRStillWaitsForAdvisoryLock(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()
	target := createTestSubnet(t, repo, "10.92.0.0/24", nil, "same CIDR")

	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin advisory blocker: %v", err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.Exec("SELECT pg_advisory_xact_lock($1)", postgres.SubnetCoordinationKey); err != nil {
		t.Fatalf("failed to hold advisory key: %v", err)
	}

	sameCIDR, _ := domain.ParseCIDR("10.92.0.0/24")
	done := make(chan error, 1)
	go func() {
		_, err := repo.Update(context.Background(), target.ID, repository.UpdateSubnet{CIDR: sameCIDR.CIDR(), CIDRSet: true})
		done <- err
	}()
	waitForPostgresCondition(t, "same-CIDR Resize advisory waiter", func() (bool, error) {
		count, err := advisoryLockCount(db, false)
		return count > 0, err
	})
	select {
	case err := <-done:
		t.Fatalf("same-CIDR Resize completed before lock release: %v", err)
	default:
	}
	if err := blocker.Commit(); err != nil {
		t.Fatalf("failed to release advisory blocker: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("same-CIDR Resize failed after lock release: %v", err)
	}
}

func TestSubnetService_InvalidCIDRWaitsForResizeLocksBeforeValidation(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	svc := service.NewSubnetService(repo)
	ctx := context.Background()
	target := createTestSubnet(t, repo, "10.95.0.0/24", nil, "invalid CIDR lock order")

	// Hold the target row independently from the advisory key so the production
	// service path can be observed at both required lock boundaries.
	rowBlocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin target row blocker: %v", err)
	}
	defer func() { _ = rowBlocker.Rollback() }()
	if _, err := rowBlocker.Exec("SELECT id FROM subnets WHERE id=$1 FOR UPDATE", target.ID); err != nil {
		t.Fatalf("failed to hold target row lock: %v", err)
	}

	advisoryBlocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin advisory blocker: %v", err)
	}
	defer func() { _ = advisoryBlocker.Rollback() }()
	if _, err := advisoryBlocker.Exec("SELECT pg_advisory_xact_lock($1)", postgres.SubnetCoordinationKey); err != nil {
		t.Fatalf("failed to hold Subnet coordination key: %v", err)
	}

	invalidCIDR := "10.95.0.1/24" // non-canonical: host bits are set
	done := make(chan error, 1)
	go func() {
		_, err := svc.UpdateSubnet(context.Background(), target.ID, service.UpdateSubnetRequest{
			CIDR: &invalidCIDR, CIDRSet: true,
		})
		done <- err
	}()

	waitForPostgresCondition(t, "invalid-CIDR PATCH waiting for advisory key before validation", func() (bool, error) {
		count, err := advisoryLockCount(db, false)
		return count > 0, err
	})
	select {
	case err := <-done:
		t.Fatalf("invalid-CIDR PATCH returned before advisory release: %v", err)
	default:
	}

	if err := advisoryBlocker.Commit(); err != nil {
		t.Fatalf("failed to release advisory blocker: %v", err)
	}
	waitForPostgresCondition(t, "invalid-CIDR PATCH holding advisory key and waiting for target row", func() (bool, error) {
		var observed bool
		err := db.QueryRow(`
			SELECT
				EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND granted)
				AND EXISTS(SELECT 1 FROM pg_locks WHERE locktype<>'advisory' AND NOT granted)
		`).Scan(&observed)
		return observed, err
	})
	select {
	case err := <-done:
		t.Fatalf("invalid-CIDR PATCH returned before target row release: %v", err)
	default:
	}

	if err := rowBlocker.Commit(); err != nil {
		t.Fatalf("failed to release target row blocker: %v", err)
	}
	if err := <-done; !errors.Is(err, domain.ErrInvalidCIDR) {
		t.Fatalf("final invalid-CIDR PATCH error = %v, want ErrInvalidCIDR", err)
	}
}

func TestSubnetRepo_ParentRowLockBlocksChildAllocationInsert(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()
	target := createTestSubnet(t, repo, "10.93.0.0/24", nil, "parent lock")

	// The isolated test trigger pauses production Resize during UPDATE. At this
	// point its earlier SELECT ... FOR UPDATE has locked the real parent row.
	// Production code is unchanged.
	const pauseKey int64 = 0x4D4F574650415553
	if _, err := db.Exec(fmt.Sprintf(`
		CREATE FUNCTION test_pause_subnet_update() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM pg_advisory_xact_lock(%d);
			RETURN NEW;
		END $$;
		CREATE TRIGGER test_pause_subnet_update_trigger
		BEFORE UPDATE ON subnets FOR EACH ROW EXECUTE FUNCTION test_pause_subnet_update();
	`, pauseKey)); err != nil {
		t.Fatalf("failed to install isolated pause trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`
			DROP TRIGGER IF EXISTS test_pause_subnet_update_trigger ON subnets;
			DROP FUNCTION IF EXISTS test_pause_subnet_update();
		`)
	})

	pauseHolder, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin pause holder: %v", err)
	}
	defer func() { _ = pauseHolder.Rollback() }()
	if _, err := pauseHolder.Exec("SELECT pg_advisory_xact_lock($1)", pauseKey); err != nil {
		t.Fatalf("failed to hold pause key: %v", err)
	}

	resizeCIDR, _ := domain.ParseCIDR("10.93.0.0/24")
	updateDone := make(chan error, 1)
	go func() {
		_, err := repo.Update(context.Background(), target.ID, repository.UpdateSubnet{
			CIDR: resizeCIDR.CIDR(), CIDRSet: true, Description: "locked parent", DescriptionSet: true,
		})
		updateDone <- err
	}()
	waitForPostgresCondition(t, "production Resize paused after parent row lock", func() (bool, error) {
		count, err := advisoryLockCount(db, false)
		return count > 0, err
	})

	childConn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("failed to pin child connection: %v", err)
	}
	defer childConn.Close()
	var childPID int
	if err := childConn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&childPID); err != nil {
		t.Fatalf("failed to obtain child backend PID: %v", err)
	}
	childDone := make(chan error, 1)
	go func() {
		_, err := childConn.ExecContext(context.Background(), `
			INSERT INTO ip_allocations (subnet_id, address, status)
			VALUES ($1, '10.93.0.10', 'reserved')
		`, target.ID)
		childDone <- err
	}()
	waitForPostgresCondition(t, "child FK check waiting on parent row lock", func() (bool, error) {
		var waitType sql.NullString
		err := db.QueryRow("SELECT wait_event_type FROM pg_stat_activity WHERE pid=$1", childPID).Scan(&waitType)
		return waitType.Valid && waitType.String == "Lock", err
	})
	select {
	case err := <-childDone:
		t.Fatalf("child INSERT completed before parent release: %v", err)
	default:
	}

	if err := pauseHolder.Commit(); err != nil {
		t.Fatalf("failed to release production Resize: %v", err)
	}
	if err := <-updateDone; err != nil {
		t.Fatalf("production Resize failed: %v", err)
	}
	if err := <-childDone; err != nil {
		t.Fatalf("child INSERT failed after parent release: %v", err)
	}

	// Future M2 allocation writers must lock/read the target Subnet, validate
	// against that locked row, then INSERT in the same transaction. A stale
	// pre-transaction read followed by INSERT is not a valid allocation workflow.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM ip_allocations WHERE subnet_id=$1", target.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unexpected child allocation result count=%d err=%v", count, err)
	}
}

func TestSubnetRepo_PartialPatchConcurrentMergePreventsLostUpdate(t *testing.T) {
	db := setupTestDB(t)
	repo := postgres.NewSubnetRepository(db)
	ctx := context.Background()
	target := createTestSubnet(t, repo, "10.94.0.0/24", nil, "original")
	var vlanID int64
	if err := db.QueryRow("INSERT INTO vlans (vlan_id, name) VALUES (94, 'Concurrent VLAN') RETURNING id").Scan(&vlanID); err != nil {
		t.Fatalf("failed to insert concurrent VLAN: %v", err)
	}

	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin row blocker: %v", err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.Exec("SELECT id FROM subnets WHERE id=$1 FOR UPDATE", target.ID); err != nil {
		t.Fatalf("failed to block partial patches: %v", err)
	}

	descriptionDone := make(chan error, 1)
	go func() {
		_, err := repo.Update(context.Background(), target.ID, repository.UpdateSubnet{Description: "description A", DescriptionSet: true})
		descriptionDone <- err
	}()
	vlanDone := make(chan error, 1)
	go func() {
		_, err := repo.Update(context.Background(), target.ID, repository.UpdateSubnet{VlanRefID: &vlanID, VlanRefIDSet: true})
		vlanDone <- err
	}()
	select {
	case err := <-descriptionDone:
		t.Fatalf("description patch completed while target row was blocked: %v", err)
	case err := <-vlanDone:
		t.Fatalf("VLAN patch completed while target row was blocked: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := blocker.Commit(); err != nil {
		t.Fatalf("failed to release partial patches: %v", err)
	}
	if err := <-descriptionDone; err != nil {
		t.Fatalf("description patch failed: %v", err)
	}
	if err := <-vlanDone; err != nil {
		t.Fatalf("VLAN patch failed: %v", err)
	}
	read, err := repo.GetByID(ctx, target.ID)
	if err != nil {
		t.Fatalf("failed to fetch merged Subnet: %v", err)
	}
	if read.Description != "description A" || read.VlanRefID == nil || *read.VlanRefID != vlanID {
		t.Fatalf("partial update lost a concurrent field: %+v", read)
	}
}
