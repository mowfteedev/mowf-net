package migrations_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/lib/pq"
)

func TestPostgresMigrations(t *testing.T) {
	config := embeddedpostgres.DefaultConfig().
		Port(9876).
		Database("mowf_net_test").
		Username("postgres").
		Password("postgres")

	postgres := embeddedpostgres.NewDatabase(config)
	if err := postgres.Start(); err != nil {
		t.Fatalf("failed to start embedded postgres: %v", err)
	}
	defer func() {
		_ = postgres.Stop()
	}()

	db, err := sql.Open("postgres", "host=localhost port=9876 user=postgres password=postgres dbname=mowf_net_test sslmode=disable")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("failed to ping database: %v", err)
	}

	// 1. Run UP migrations
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

	// 2. Valid IPv4 subnet insert succeeds
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

	// 3. IPv6 is rejected by CHECK (family(network) = 4)
	_, err = db.Exec(`
		INSERT INTO subnets (network, prefix_length, description)
		VALUES ('2001:db8::'::inet, 24, 'IPv6 Subnet')
	`)
	if err == nil {
		t.Fatalf("expected error inserting IPv6 into subnets, got nil")
	}
	t.Logf("IPv6 correctly rejected: %v", err)

	// 4. network with mask other than /32 is rejected by CHECK (masklen(network) = 32)
	_, err = db.Exec(`
		INSERT INTO subnets (network, prefix_length, description)
		VALUES ('192.168.2.0/24'::inet, 24, 'Subnet with embedded mask')
	`)
	if err == nil {
		t.Fatalf("expected error inserting network with masklen != 32, got nil")
	}
	t.Logf("Network with masklen != 32 correctly rejected: %v", err)

	// 5. prefix /31 is rejected by CHECK (prefix_length BETWEEN 1 AND 30)
	_, err = db.Exec(`
		INSERT INTO subnets (network, prefix_length, description)
		VALUES ('192.168.3.0'::inet, 31, 'Prefix 31 Subnet')
	`)
	if err == nil {
		t.Fatalf("expected error inserting prefix /31, got nil")
	}
	t.Logf("Prefix /31 correctly rejected: %v", err)

	// prefix /0 is rejected
	_, err = db.Exec(`
		INSERT INTO subnets (network, prefix_length, description)
		VALUES ('192.168.3.0'::inet, 0, 'Prefix 0 Subnet')
	`)
	if err == nil {
		t.Fatalf("expected error inserting prefix /0, got nil")
	}
	t.Logf("Prefix /0 correctly rejected: %v", err)

	// 6. duplicate (network, prefix_length) is rejected by UNIQUE constraint
	_, err = db.Exec(`
		INSERT INTO subnets (network, prefix_length, description)
		VALUES ('192.168.1.0'::inet, 24, 'Duplicate Subnet')
	`)
	if err == nil {
		t.Fatalf("expected error inserting duplicate (network, prefix_length), got nil")
	}
	t.Logf("Duplicate (network, prefix_length) correctly rejected: %v", err)

	// 7. vlan_ref_id non-existent is rejected by FK constraint
	_, err = db.Exec(`
		INSERT INTO subnets (vlan_ref_id, network, prefix_length, description)
		VALUES (99999, '192.168.4.0'::inet, 24, 'Invalid VLAN FK')
	`)
	if err == nil {
		t.Fatalf("expected error inserting invalid vlan_ref_id FK, got nil")
	}
	t.Logf("Invalid vlan_ref_id FK correctly rejected: %v", err)

	// Valid vlan_ref_id succeeds
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

	// 8. Run DOWN migrations
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

	t.Log("DOWN migrations completed successfully")
}
