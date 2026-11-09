CREATE TABLE hourly_prices (
    id        uuid PRIMARY KEY,
    provider  provider NOT NULL UNIQUE,
    cpu_cores bigint NOT NULL CHECK (cpu_cores >= 0),
    ram_gb    bigint NOT NULL CHECK (ram_gb >= 0),
    disk_gb   bigint NOT NULL CHECK (disk_gb >= 0)
);
