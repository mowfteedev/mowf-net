CREATE TABLE IF NOT EXISTS subnets (
    id BIGINT PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
    vlan_ref_id BIGINT REFERENCES vlans(id) ON DELETE RESTRICT,
    network INET NOT NULL,
    prefix_length SMALLINT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT subnets_network_ipv4_chk CHECK (family(network) = 4),
    CONSTRAINT subnets_network_hostmask_chk CHECK (masklen(network) = 32),
    CONSTRAINT subnets_prefix_length_chk CHECK (prefix_length BETWEEN 1 AND 30),
    CONSTRAINT subnets_network_prefix_uq UNIQUE (network, prefix_length)
);
