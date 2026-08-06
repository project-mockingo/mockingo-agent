CREATE TABLE IF NOT EXISTS endpoints (
    id UUID PRIMARY KEY,
    name VARCHAR(40) NOT NULL,
    hostname TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT endpoints_name_unique UNIQUE (name),
    CONSTRAINT endpoints_hostname_unique UNIQUE (hostname)
);
