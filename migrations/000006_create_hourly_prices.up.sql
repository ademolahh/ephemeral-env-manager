CREATE TABLE hourly_prices (
    id        uuid PRIMARY KEY,
    provider  text NOT NULL UNIQUE CHECK (provider <> ''),
    cpu_cores bigint NOT NULL CHECK (cpu_cores >= 0),
    ram_gb    bigint NOT NULL CHECK (ram_gb >= 0),
    disk_gb   bigint NOT NULL CHECK (disk_gb >= 0)
);
