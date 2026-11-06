# Ephemeral Environment Manager

Creates short-lived, isolated environments on Docker or in the cloud for testing, reviewing PRs and reproducing production bugs. Humans use it through an API and agents through MCP.

![EEM architecture](assets/image.png)

Callers provide a workload image and choose `REGULAR_TESTING`, `PR_REVIEW`, or `PROD_BUG`. EEM checks permissions, resource quota, provider capacity, and cost budget (except for `PROD_BUG`) before provisioning. Background workers delete expired environments and recover stuck provisioning or deletion.
