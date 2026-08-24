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

func TestSubnetRepo_Create_ConcurrencySerialization(t *testing.T) {
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

func TestSubnetRepo_Create_MultiConcurrencyStress(t *testing.T) {
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
