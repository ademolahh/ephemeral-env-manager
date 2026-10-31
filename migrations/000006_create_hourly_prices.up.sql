CREATE TABLE hourly_prices (
    id        uuid PRIMARY KEY,
    provider  provider NOT NULL UNIQUE,
    cpu_cores numeric NOT NULL CHECK (cpu_cores >= 0),
    ram_gb    numeric NOT NULL CHECK (ram_gb >= 0),
    disk_gb   numeric NOT NULL CHECK (disk_gb >= 0)
);
