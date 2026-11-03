# Ephemeral Environment Manager

Creates short-lived, isolated environments on Docker or in the cloud for testing, reviewing PRs and reproducing production bugs. Humans use it through an API and agents through MCP.

## Web dashboard

The server serves a small dashboard at `http://localhost:8080/`. Enter an existing EEM API key to view the environments owned by that key's caller. The key is kept in the browser tab's session storage and cleared when you sign out.

The dashboard uses `GET /envs` with the same `Authorization: Bearer <api-key>` header as the rest of the API. This endpoint returns the caller's environments, newest first, including deleted and failed environments.

The dashboard also shows the caller's USD cost budget, the amount used in the current budget period, and current resource usage. It reads `GET /callers/me/budget`, which identifies the currency as `USD`; the used amount follows the same calculation as environment creation, including planned lifetime cost for active environments.

## Configure Docker

After running the database migrations, configure Docker's pricing and capacity before creating environments:

```sh
make seed-docker
```

This sets local Docker hourly prices to $0.004048 per CPU core, $0.004445 per GB of RAM, and $0.00011 per GB of disk, as specified in the project doc. It also sets a logical capacity of 100 CPU cores, 100 GB RAM, and 1000 GB disk. Use `ARGS` to choose values, for example `make seed-docker ARGS="-cpu 8 -ram 16 -disk 100 -cpu-price 0.01"`. Prices are in USD per hour. Rerunning the command updates the stored prices and capacity while preserving currently reserved capacity.

## Generate an API key

After configuring `.env` and running the database migrations, generate a key with:

```sh
make api-key
```

This creates an admin caller with access to all environment types, a resource quota matching the configured Docker pool, and an initial USD cost budget of $100 for 30 days. It prints a key valid for 30 days. To generate a key for a caller that already owns environments:

```sh
make api-key ARGS="-caller-id <caller-uuid>"
```

For an existing caller, the script upgrades that caller to admin and sets its resource quota to the configured Docker pool capacity, but keeps its current USD cost budget and budget period. The role and quota changes affect all API keys belonging to that caller. The script prints the new API key once. Paste it into the dashboard at `http://localhost:8080/`.
