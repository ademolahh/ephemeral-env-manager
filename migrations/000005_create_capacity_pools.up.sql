CREATE TABLE capacity_pools (
    id                  uuid PRIMARY KEY,
    provider            provider NOT NULL UNIQUE,
    default_cpu_cores   integer NOT NULL CHECK (default_cpu_cores >= 0),
    default_ram_gb      integer NOT NULL CHECK (default_ram_gb >= 0),
    default_disk_gb     integer NOT NULL CHECK (default_disk_gb >= 0),
    available_cpu_cores integer NOT NULL CHECK (available_cpu_cores BETWEEN 0 AND default_cpu_cores),
    available_ram_gb    integer NOT NULL CHECK (available_ram_gb BETWEEN 0 AND default_ram_gb),
    available_disk_gb   integer NOT NULL CHECK (available_disk_gb BETWEEN 0 AND default_disk_gb)
);
