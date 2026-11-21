CREATE TABLE capacity_pools (
    id                   uuid PRIMARY KEY,
    provider             provider NOT NULL UNIQUE,
    default_cpu_millis   integer NOT NULL CHECK (default_cpu_millis >= 0),
    default_ram_mb       integer NOT NULL CHECK (default_ram_mb >= 0),
    default_disk_gb      integer NOT NULL CHECK (default_disk_gb >= 0),
    available_cpu_millis integer NOT NULL CHECK (available_cpu_millis BETWEEN 0 AND default_cpu_millis),
    available_ram_mb     integer NOT NULL CHECK (available_ram_mb BETWEEN 0 AND default_ram_mb),
    available_disk_gb    integer NOT NULL CHECK (available_disk_gb BETWEEN 0 AND default_disk_gb)
);
