package migrations_test

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/lib/pq"
	_ "github.com/lib/pq"
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

func assertPQError(t *testing.T, err error, expectedState pq.ErrorCode, expectedConstraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with SQLSTATE %s (%s), got nil", expectedState, expectedConstraint)
	}
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		t.Fatalf("expected *pq.Error, got %T: %v", err, err)
	}
	if pqErr.Code != expectedState {
		t.Errorf("expected SQLSTATE %s, got %s (err: %v)", expectedState, pqErr.Code, err)
	}
	if pqErr.Constraint != expectedConstraint {
		t.Errorf("expected constraint %q, got %q (err: %v)", expectedConstraint, pqErr.Constraint, err)
	}
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

	postgres := embeddedpostgres.NewDatabase(config)
	if err := postgres.Start(); err != nil {
		t.Fatalf("failed to start isolated embedded postgres on port %d: %v", port, err)
	}

	t.Cleanup(func() {
		_ = postgres.Stop()
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
		t.Fatalf("failed to ping isolated database on port %d: %v", port, err)
	}

	return db
}

func TestPostgresMigrations(t *testing.T) {
	db := setupTestDB(t)

	// 1. PostgreSQL runtime version evidence
	var serverVersion, serverVersionNum string
	if err := db.QueryRow("SHOW server_version").Scan(&serverVersion); err != nil {
		t.Fatalf("failed to query server_version: %v", err)
	}
	if err := db.QueryRow("SHOW server_version_num").Scan(&serverVersionNum); err != nil {
		t.Fatalf("failed to query server_version_num: %v", err)
	}
	t.Logf("PostgreSQL runtime evidence: server_version=%s, server_version_num=%s", serverVersion, serverVersionNum)

	// 2. Run UP migrations
	vlanUp, err := os.ReadFile(filepath.Join(".", "000001_create_vlans_table.up.sql"))
	if err != nil {
		t.Fatalf("failed to read vlan up migration: %v", err)
	}
	if _, err := db.Exec(string(vlanUp)); err != nil {
		t.Fatalf("failed to execute vlan up migration: %v", err)
	}

	subnetUp, err := os.ReadFile(filepath.Join(".", "000002_create_subnets_table.up.sql"))
	if err != nil {
		t.Fatalf("failed to read subnet up migration: %v", err)
	}
	if _, err := db.Exec(string(subnetUp)); err != nil {
		t.Fatalf("failed to execute subnet up migration: %v", err)
	}

	t.Log("UP migrations completed successfully")

	// 3. Valid IPv4 subnet insert succeeds
	var subnetID int64
	err = db.QueryRow(`
		INSERT INTO subnets (network, prefix_length, description)
		VALUES ('192.168.1.0'::inet, 24, 'Main Subnet')
		RETURNING id
	`).Scan(&subnetID)
	if err != nil {
		t.Fatalf("failed to insert valid IPv4 subnet: %v", err)
	}
	if subnetID <= 0 {
		t.Errorf("expected positive subnet id, got %d", subnetID)
	}

	// 4. Constraint tests with exact *pq.Error assertions

	// 4a. IPv6 is rejected by subnets_network_ipv4_chk (SQLSTATE 23514)
	_, err = db.Exec(`
		INSERT INTO subnets (network, prefix_length, description)
		VALUES ('2001:db8::/32'::inet, 24, 'IPv6 Subnet')
	`)
	assertPQError(t, err, "23514", "subnets_network_ipv4_chk")
	t.Log("IPv6 correctly rejected by subnets_network_ipv4_chk (23514)")

	// 4b. network with masklen != 32 is rejected by subnets_network_hostmask_chk (SQLSTATE 23514)
	_, err = db.Exec(`
		INSERT INTO subnets (network, prefix_length, description)
		VALUES ('192.168.2.0/24'::inet, 24, 'Subnet with embedded mask')
	`)
	assertPQError(t, err, "23514", "subnets_network_hostmask_chk")
	t.Log("Network masklen != 32 correctly rejected by subnets_network_hostmask_chk (23514)")

	// 4c. prefix /31 is rejected by subnets_prefix_length_chk (SQLSTATE 23514)
	_, err = db.Exec(`
		INSERT INTO subnets (network, prefix_length, description)
		VALUES ('192.168.3.0'::inet, 31, 'Prefix 31 Subnet')
	`)
	assertPQError(t, err, "23514", "subnets_prefix_length_chk")
	t.Log("Prefix /31 correctly rejected by subnets_prefix_length_chk (23514)")

	// 4d. prefix /0 is rejected by subnets_prefix_length_chk (SQLSTATE 23514)
	_, err = db.Exec(`
		INSERT INTO subnets (network, prefix_length, description)
		VALUES ('192.168.3.0'::inet, 0, 'Prefix 0 Subnet')
	`)
	assertPQError(t, err, "23514", "subnets_prefix_length_chk")
	t.Log("Prefix /0 correctly rejected by subnets_prefix_length_chk (23514)")

	// 4e. duplicate (network, prefix_length) is rejected by subnets_network_prefix_uq (SQLSTATE 23505)
	_, err = db.Exec(`
		INSERT INTO subnets (network, prefix_length, description)
		VALUES ('192.168.1.0'::inet, 24, 'Duplicate Subnet')
	`)
	assertPQError(t, err, "23505", "subnets_network_prefix_uq")
	t.Log("Duplicate (network, prefix_length) correctly rejected by subnets_network_prefix_uq (23505)")

	// 4f. invalid vlan_ref_id is rejected by subnets_vlan_ref_id_fkey (SQLSTATE 23503)
	_, err = db.Exec(`
		INSERT INTO subnets (vlan_ref_id, network, prefix_length, description)
		VALUES (99999, '192.168.4.0'::inet, 24, 'Invalid VLAN FK')
	`)
	assertPQError(t, err, "23503", "subnets_vlan_ref_id_fkey")
	t.Log("Invalid vlan_ref_id correctly rejected by subnets_vlan_ref_id_fkey (23503)")

	// Valid vlan_ref_id insert succeeds
	var vlanID int64
	err = db.QueryRow(`
		INSERT INTO vlans (vlan_id, name, description)
		VALUES (100, 'Test VLAN', 'VLAN 100')
		RETURNING id
	`).Scan(&vlanID)
	if err != nil {
		t.Fatalf("failed to insert valid vlan: %v", err)
	}

	var subnetWithVlanID int64
	err = db.QueryRow(`
		INSERT INTO subnets (vlan_ref_id, network, prefix_length, description)
		VALUES ($1, '192.168.4.0'::inet, 24, 'Subnet with VLAN')
		RETURNING id
	`, vlanID).Scan(&subnetWithVlanID)
	if err != nil {
		t.Fatalf("failed to insert subnet with valid vlan_ref_id: %v", err)
	}
	t.Log("Valid vlan_ref_id association succeeded")

	// 5. Run DOWN migrations
	subnetDown, err := os.ReadFile(filepath.Join(".", "000002_create_subnets_table.down.sql"))
	if err != nil {
		t.Fatalf("failed to read subnet down migration: %v", err)
	}
	if _, err := db.Exec(string(subnetDown)); err != nil {
		t.Fatalf("failed to execute subnet down migration: %v", err)
	}

	vlanDown, err := os.ReadFile(filepath.Join(".", "000001_create_vlans_table.down.sql"))
	if err != nil {
		t.Fatalf("failed to read vlan down migration: %v", err)
	}
	if _, err := db.Exec(string(vlanDown)); err != nil {
		t.Fatalf("failed to execute vlan down migration: %v", err)
	}

	// 6. Verify table absence via to_regclass
	var subnetsRegClass sql.NullString
	if err := db.QueryRow("SELECT to_regclass('public.subnets')").Scan(&subnetsRegClass); err != nil {
		t.Fatalf("failed to query to_regclass('public.subnets'): %v", err)
	}
	if subnetsRegClass.Valid {
		t.Fatalf("expected public.subnets table to be dropped, got regclass: %s", subnetsRegClass.String)
	}

	var vlansRegClass sql.NullString
	if err := db.QueryRow("SELECT to_regclass('public.vlans')").Scan(&vlansRegClass); err != nil {
		t.Fatalf("failed to query to_regclass('public.vlans'): %v", err)
	}
	if vlansRegClass.Valid {
		t.Fatalf("expected public.vlans table to be dropped, got regclass: %s", vlansRegClass.String)
	}

	t.Log("DOWN migrations verified: public.subnets and public.vlans confirmed dropped")
}
