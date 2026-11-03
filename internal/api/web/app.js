const keyStorage = "eem_api_key";
const pageSize = 12;
const $ = (id) => document.getElementById(id);
let apiKey = sessionStorage.getItem(keyStorage) || "";
let pendingDelete = null;
let currentPage = 1;

async function requestJSON(path, method = "GET") {
  const response = await fetch(path, {
    method,
    cache: "no-store",
    headers: { Authorization: `Bearer ${apiKey}` },
  });
  if (!response.ok) {
    let message = `Request failed (${response.status})`;
    try {
      message = (await response.json()).error || message;
    } catch (_) {}
    const error = new Error(message);
    error.status = response.status;
    throw error;
  }
  return response.json();
}

const listEnvironments = (page = 1) => {
  const params = new URLSearchParams({ page: String(page), limit: String(pageSize) });
  const status = $("status-filter").value;
  if (status !== "all") params.set("status", status);
  return requestJSON(`/envs?${params}`);
};
const getBudget = () => requestJSON("/callers/me/budget");
const getCapacity = () => requestJSON("/capacity");
const deleteEnvironment = (id) =>
  requestJSON(`/envs/${encodeURIComponent(id)}`, "DELETE");

function showLogin(message = "") {
  $("login-view").hidden = false;
  $("dashboard-view").hidden = true;
  $("account").hidden = true;
  $("caller-id").textContent = "—";
  $("status-filter").value = "all";
  currentPage = 1;
  $("pagination").hidden = true;
  $("login-error").textContent = message;
  $("login-error").hidden = !message;
  $("budget-given").textContent = "—";
  $("budget-used").textContent = "—";
  $("budget-remaining").textContent = "—";
  $("prod-bug-cost-used").textContent = "";
  $("budget-expiry").textContent = "Loading budget…";
  $("budget-error").hidden = true;
  $("budget-fill").style.width = "0%";
  $("budget-fill").parentElement.setAttribute("aria-valuenow", "0");
  $("capacity-pools").replaceChildren();
  $("capacity-error").hidden = true;
}

function showDashboard() {
  $("login-view").hidden = true;
  $("dashboard-view").hidden = false;
  $("account").hidden = false;
}

function readableDate(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "—"
    : new Intl.DateTimeFormat(undefined, {
        month: "short",
        day: "numeric",
        year: "numeric",
        hour: "numeric",
        minute: "2-digit",
        second: "2-digit",
        timeZoneName: "short",
      }).format(date);
}

function formatUSD(value) {
  const match = String(value).match(/^(\d+)(?:\.(\d+))?$/);
  if (!match) return "—";
  const whole = BigInt(match[1]).toLocaleString("en-US");
  const fraction = (match[2] || "")
    .slice(0, 6)
    .replace(/0+$/, "")
    .padEnd(2, "0");
  return `$${whole}.${fraction}`;
}

function renderBudget(budget) {
  const given = Number(budget.cost_budget);
  const used = Number(budget.cost_used);
  $("caller-id").textContent = budget.caller_id || "—";
  $("budget-given").textContent = formatUSD(budget.cost_budget);
  $("budget-used").textContent = formatUSD(budget.cost_used);
  $("budget-remaining").textContent = formatUSD(budget.cost_remaining);
  $("prod-bug-cost-used").textContent =
    `PROD_BUG cost (outside budget): ${formatUSD(budget.prod_bug_cost_used)}`;
  $("budget-expiry").textContent =
    `Expires ${readableDate(budget.budget_expires_at)}`;
  const percent =
    given > 0
      ? Math.min(100, Math.max(0, (used / given) * 100))
      : used > 0
        ? 100
        : 0;
  $("budget-fill").style.width = `${percent}%`;
  $("budget-fill").parentElement.setAttribute(
    "aria-valuenow",
    String(Math.round(percent)),
  );
  $("budget-error").hidden = true;
}

const capacityResources = [
  ["cpu_core", "CPU", ""],
  ["ram_gb", "RAM", " GB"],
  ["disk_gb", "Disk", " GB"],
];

function capacityRow(pool, key, label, unit) {
  const total = pool.total[key] || 0;
  const used = pool.used[key] || 0;
  const limit = pool.normal_limit[key] || 0;
  const pct = (n) => (total > 0 ? Math.min(100, Math.max(0, (n / total) * 100)) : 0);

  const row = document.createElement("div");
  row.className = "capacity-row";
  const name = document.createElement("span");
  name.className = "capacity-label";
  name.textContent = label;

  const track = document.createElement("div");
  track.className = "capacity-track";
  track.setAttribute("role", "progressbar");
  track.setAttribute("aria-label", `${pool.provider} ${label} used`);
  track.setAttribute("aria-valuemin", "0");
  track.setAttribute("aria-valuemax", String(total));
  track.setAttribute("aria-valuenow", String(used));
  const reserved = document.createElement("span");
  reserved.className = "capacity-reserved";
  reserved.style.left = `${pct(limit)}%`;
  reserved.style.width = `${100 - pct(limit)}%`;
  const fill = document.createElement("span");
  fill.className = used > limit ? "capacity-fill over" : "capacity-fill";
  fill.style.width = `${pct(used)}%`;
  const marker = document.createElement("span");
  marker.className = "capacity-marker";
  marker.style.left = `${pct(limit)}%`;
  marker.title = `Normal limit: ${limit}${unit}`;
  track.append(reserved, fill, marker);

  const detail = document.createElement("span");
  detail.className = "capacity-detail";
  detail.textContent =
    `${used} / ${total}${unit} used · ${pool.normal_free[key]}${unit} free for normal · ${pool.prod_bug_free[key]}${unit} for PROD_BUG`;

  row.append(name, track, detail);
  return row;
}

function renderCapacity(capacity) {
  const container = $("capacity-pools");
  container.replaceChildren();
  for (const pool of capacity.pools) {
    const card = document.createElement("div");
    card.className = "capacity-pool";
    const title = document.createElement("h3");
    title.textContent = pool.provider;
    card.append(title);
    for (const [key, label, unit] of capacityResources) {
      card.append(capacityRow(pool, key, label, unit));
    }
    container.append(card);
  }
  if (capacity.pools.length === 0) {
    const empty = document.createElement("p");
    empty.className = "budget-resources";
    empty.textContent = "No provider capacity has been configured.";
    container.append(empty);
  }
  $("capacity-error").hidden = true;
}

async function loadCapacity() {
  try {
    renderCapacity(await getCapacity());
  } catch (error) {
    if (error.status === 401) return;
    $("capacity-error").textContent = `Could not load capacity: ${error.message}`;
    $("capacity-error").hidden = false;
  }
}

async function loadBudget() {
  try {
    renderBudget(await getBudget());
  } catch (error) {
    if (error.status === 401) {
      sessionStorage.removeItem(keyStorage);
      apiKey = "";
      showLogin("Your API key is invalid or expired.");
      return;
    }
    $("budget-error").textContent = `Could not load budget: ${error.message}`;
    $("budget-error").hidden = false;
  }
}

function environmentDetail(label, value) {
  const detail = document.createElement("div");
  detail.className = "env-detail";
  const caption = document.createElement("span");
  caption.textContent = label;
  const content = document.createElement("strong");
  content.textContent = value;
  detail.append(caption, content);
  return detail;
}

function showDeleteDialog(env) {
  const retry = env.status === "DELETING";
  pendingDelete = env;
  $("delete-dialog-title").textContent = retry
    ? "Retry deleting environment?"
    : "Delete environment?";
  $("delete-dialog-description").textContent = retry
    ? `Retry deleting ${env.template_name || "Environment"} (${env.id}) and its services?`
    : `Delete ${env.template_name || "Environment"} (${env.id}) and stop its services?`;
  $("delete-confirm").textContent = retry ? "Retry delete" : "Delete environment";
  $("delete-dialog-error").hidden = true;
  $("delete-dialog").showModal();
}

async function confirmDelete() {
  const env = pendingDelete;
  if (!env) return;
  const button = $("delete-confirm");
  button.disabled = true;
  $("delete-cancel").disabled = true;
  button.textContent = env.status === "DELETING" ? "Retrying…" : "Deleting…";
  try {
    await deleteEnvironment(env.id);
    $("delete-dialog").close();
    await loadEnvironments(currentPage);
  } catch (error) {
    if (error.status === 401) {
      apiKey = "";
      sessionStorage.removeItem(keyStorage);
      $("delete-dialog").close();
      showLogin("Your API key is invalid or expired.");
      return;
    }
    const errorMessage = $("delete-dialog-error");
    errorMessage.textContent = `Could not delete environment: ${error.message}`;
    errorMessage.hidden = false;
  } finally {
    button.disabled = false;
    $("delete-cancel").disabled = false;
    button.textContent = env.status === "DELETING" ? "Retry delete" : "Delete environment";
  }
}

function renderEnvironments(page) {
  currentPage = page.page;
  const filter = $("status-filter").value;
  const filteredTotal = filter === "all" ? page.total : (page.counts[filter] ?? 0);
  const totalPages = Math.ceil(filteredTotal / page.limit);
  $("pagination").hidden = filteredTotal === 0;
  $("page-number").textContent = `Page ${page.page} of ${totalPages}`;
  $("previous-page").disabled = page.page <= 1;
  $("next-page").disabled = !page.next_page;
  $("total-count").textContent = page.total;
  $("running-count").textContent = page.counts.RUNNING ?? 0;
  $("provisioning-count").textContent = page.counts.PROVISIONING ?? 0;
  const grid = $("environment-grid");
  grid.replaceChildren();
  for (const env of page.environments) {
    const card = document.createElement("article");
    card.className = "env-card";
    const header = document.createElement("div");
    header.className = "env-card-head";
    const identity = document.createElement("div");
    identity.className = "env-identity";
    const name = document.createElement("h3");
    name.className = "env-name";
    name.textContent =
      `${env.template_name || "Environment"} ${env.template_version || ""}`.trim();
    const id = document.createElement("span");
    id.className = "env-id";
    id.textContent = env.id || "";
    identity.append(name, id);
    const status = document.createElement("span");
    status.className = `status-badge ${(env.status || "").toLowerCase()}`;
    status.textContent = (env.status || "UNKNOWN").replaceAll("_", " ");
    header.append(identity, status);

    const res = env.resources || {};
    const details = document.createElement("div");
    details.className = "env-card-details";
    details.append(
      environmentDetail("Provider", env.provider || "—"),
      environmentDetail("Resources", `${res.cpu_core ?? 0} CPU · ${res.ram_gb ?? 0} GB RAM · ${res.disk_gb ?? 0} GB disk`),
      environmentDetail("Created", readableDate(env.created_at)),
      environmentDetail("Expires", readableDate(env.expires_at)),
    );

    const actions = document.createElement("div");
    actions.className = "env-card-actions";
    if (env.endpoint && /^https?:\/\//i.test(env.endpoint)) {
      const link = document.createElement("a");
      link.className = "endpoint";
      link.href = env.endpoint;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      link.textContent = "Open environment ↗";
      actions.append(link);
    }
    if (["PROVISIONING", "RUNNING", "DELETING"].includes(env.status)) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "delete-button";
      button.textContent = env.status === "DELETING" ? "Retry delete" : "Delete";
      button.setAttribute("aria-label", `${button.textContent} environment ${env.id}`);
      button.addEventListener("click", () => showDeleteDialog(env));
      actions.append(button);
    }
    card.append(header, details);
    if (actions.childElementCount) card.append(actions);
    grid.append(card);
  }
  grid.hidden = page.environments.length === 0;
  $("list-status").hidden = page.environments.length > 0;
  $("list-status").className = "list-status";
  $("list-status").textContent = page.total === 0
    ? "No environments yet. Environments created with this account will appear here."
    : page.environments.length === 0
      ? `No ${$("status-filter").selectedOptions[0].textContent.toLowerCase()} environments.`
      : "";
}

async function loadEnvironments(requestedPage = 1) {
  $("refresh").disabled = true;
  $("list-status").hidden = false;
  $("list-status").className = "list-status";
  $("list-status").textContent = "Loading environments…";
  $("pagination").hidden = true;
  try {
    let page = await listEnvironments(requestedPage);
    const filter = $("status-filter").value;
    const filteredTotal = filter === "all" ? page.total : (page.counts[filter] ?? 0);
    if (requestedPage > 1 && filteredTotal > 0 && page.environments.length === 0) {
      page = await listEnvironments(Math.ceil(filteredTotal / page.limit));
    }
    renderEnvironments(page);
    await Promise.all([loadBudget(), loadCapacity()]);
  } catch (error) {
    if (error.status === 401) {
      sessionStorage.removeItem(keyStorage);
      apiKey = "";
      showLogin("Your API key is invalid or expired.");
      return;
    }
    $("environment-grid").hidden = true;
    $("list-status").className = "list-status error";
    $("list-status").textContent =
      `Could not load environments: ${error.message}`;
  } finally {
    $("refresh").disabled = false;
  }
}

$("key-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const value = $("api-key").value.trim();
  if (!value) return;
  const button = $("key-form").querySelector('button[type="submit"]');
  button.disabled = true;
  apiKey = value;
  try {
    const page = await listEnvironments();
    sessionStorage.setItem(keyStorage, apiKey);
    $("api-key").value = "";
    showDashboard();
    renderEnvironments(page);
    await Promise.all([loadBudget(), loadCapacity()]);
  } catch (error) {
    apiKey = "";
    showLogin(
      error.status === 401
        ? "That API key is invalid or expired."
        : `Could not sign in: ${error.message}`,
    );
  } finally {
    button.disabled = false;
  }
});

$("toggle-key").addEventListener("click", () => {
  const input = $("api-key");
  input.type = input.type === "password" ? "text" : "password";
  $("toggle-key").textContent = input.type === "password" ? "Show" : "Hide";
  $("toggle-key").setAttribute(
    "aria-label",
    `${input.type === "password" ? "Show" : "Hide"} API key`,
  );
});

$("refresh").addEventListener("click", () => loadEnvironments(currentPage));
$("status-filter").addEventListener("change", () => loadEnvironments());
$("previous-page").addEventListener("click", () => loadEnvironments(currentPage - 1));
$("next-page").addEventListener("click", () => loadEnvironments(currentPage + 1));
$("delete-cancel").addEventListener("click", () => $("delete-dialog").close());
$("delete-confirm").addEventListener("click", confirmDelete);
$("delete-dialog").addEventListener("close", () => { pendingDelete = null; });
$("delete-dialog").addEventListener("cancel", (event) => {
  if ($("delete-confirm").disabled) event.preventDefault();
});
$("sign-out").addEventListener("click", () => {
  apiKey = "";
  sessionStorage.removeItem(keyStorage);
  showLogin();
});

if (apiKey) {
  showDashboard();
  loadEnvironments();
}
