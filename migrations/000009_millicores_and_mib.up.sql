-- Resources move from whole CPU cores and GB of RAM to millicores and MiB,
-- so an environment can ask for half a core or 512 MiB. Disk stays in GB.
-- Prices in hourly_prices stay per core-hour and per GB-hour.

ALTER TABLE environments RENAME COLUMN cpu_cores TO cpu_millis;
ALTER TABLE environments RENAME COLUMN ram_gb TO ram_mb;
UPDATE environments SET cpu_millis = cpu_millis * 1000, ram_mb = ram_mb * 1024;

ALTER TABLE capacity_pools RENAME COLUMN default_cpu_cores TO default_cpu_millis;
ALTER TABLE capacity_pools RENAME COLUMN default_ram_gb TO default_ram_mb;
ALTER TABLE capacity_pools RENAME COLUMN available_cpu_cores TO available_cpu_millis;
ALTER TABLE capacity_pools RENAME COLUMN available_ram_gb TO available_ram_mb;
UPDATE capacity_pools SET
    default_cpu_millis   = default_cpu_millis * 1000,
    available_cpu_millis = available_cpu_millis * 1000,
    default_ram_mb       = default_ram_mb * 1024,
    available_ram_mb     = available_ram_mb * 1024;

ALTER TABLE callers RENAME COLUMN quota_cpu_cores TO quota_cpu_millis;
ALTER TABLE callers RENAME COLUMN quota_ram_gb TO quota_ram_mb;
UPDATE callers SET quota_cpu_millis = quota_cpu_millis * 1000, quota_ram_mb = quota_ram_mb * 1024;
