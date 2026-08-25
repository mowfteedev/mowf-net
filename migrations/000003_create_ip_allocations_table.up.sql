CREATE TABLE ip_allocations (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    subnet_id BIGINT NOT NULL,

    address INET NOT NULL,

    status TEXT NOT NULL,

    interface_id BIGINT NULL,

    description TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT ip_allocations_subnet_id_fkey
        FOREIGN KEY (subnet_id)
        REFERENCES subnets(id)
        ON DELETE RESTRICT,

    CONSTRAINT ip_allocations_address_ipv4_chk
        CHECK (family(address) = 4),

    CONSTRAINT ip_allocations_address_hostmask_chk
        CHECK (masklen(address) = 32),

    CONSTRAINT ip_allocations_status_chk
        CHECK (status IN ('reserved', 'assigned')),

    CONSTRAINT ip_allocations_status_interface_chk
        CHECK (
            (status = 'reserved' AND interface_id IS NULL)
            OR
            (status = 'assigned' AND interface_id IS NOT NULL)
        ),

    CONSTRAINT ip_allocations_address_uq
        UNIQUE (address)
);

CREATE INDEX ip_allocations_subnet_id_idx
    ON ip_allocations(subnet_id);
