-- Environments round partial cores and GB up so none shrinks below what it used;
-- quotas and pools round down.

UPDATE callers SET quota_cpu_millis = quota_cpu_millis / 1000, quota_ram_mb = quota_ram_mb / 1024;
ALTER TABLE callers RENAME COLUMN quota_cpu_millis TO quota_cpu_cores;
ALTER TABLE callers RENAME COLUMN quota_ram_mb TO quota_ram_gb;

UPDATE capacity_pools SET
    default_cpu_millis   = default_cpu_millis / 1000,
    available_cpu_millis = available_cpu_millis / 1000,
    default_ram_mb       = default_ram_mb / 1024,
    available_ram_mb     = available_ram_mb / 1024;
ALTER TABLE capacity_pools RENAME COLUMN default_cpu_millis TO default_cpu_cores;
ALTER TABLE capacity_pools RENAME COLUMN default_ram_mb TO default_ram_gb;
ALTER TABLE capacity_pools RENAME COLUMN available_cpu_millis TO available_cpu_cores;
ALTER TABLE capacity_pools RENAME COLUMN available_ram_mb TO available_ram_gb;

UPDATE environments SET cpu_millis = (cpu_millis + 999) / 1000, ram_mb = (ram_mb + 1023) / 1024;
ALTER TABLE environments RENAME COLUMN cpu_millis TO cpu_cores;
ALTER TABLE environments RENAME COLUMN ram_mb TO ram_gb;
