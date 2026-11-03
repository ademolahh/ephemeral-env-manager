const keyStorage = "eem_api_key";
const $ = (id) => document.getElementById(id);
let apiKey = sessionStorage.getItem(keyStorage) || "";
let pendingDelete = null;

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

const listEnvironments = () => requestJSON("/envs");
const getBudget = () => requestJSON("/callers/me/budget");
const deleteEnvironment = (id) =>
  requestJSON(`/envs/${encodeURIComponent(id)}`, "DELETE");

function showLogin(message = "") {
  $("login-view").hidden = false;
  $("dashboard-view").hidden = true;
  $("account").hidden = true;
  $("login-error").textContent = message;
  $("login-error").hidden = !message;
  $("budget-given").textContent = "—";
  $("budget-used").textContent = "—";
  $("budget-remaining").textContent = "—";
  $("budget-expiry").textContent = "Loading budget…";
  $("budget-resources-used").textContent = "";
  $("budget-resources-assigned").textContent = "";
  $("budget-error").hidden = true;
  $("budget-fill").style.width = "0%";
  $("budget-fill").parentElement.setAttribute("aria-valuenow", "0");
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

function formatResourceAmount(value, unit) {
  return `${new Intl.NumberFormat().format(value ?? 0)} ${unit}`;
}

function renderBudget(budget) {
  const given = Number(budget.cost_budget);
  const used = Number(budget.cost_used);
  $("budget-given").textContent = formatUSD(budget.cost_budget);
  $("budget-used").textContent = formatUSD(budget.cost_used);
  $("budget-remaining").textContent = formatUSD(budget.cost_remaining);
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
  const quota = budget.quota || {};
  const resources = budget.resources_used || {};
  $("budget-resources-used").textContent =
    `Resources used: ${formatResourceAmount(resources.cpu_core, "CPU")} · ${formatResourceAmount(resources.ram_gb, "GB RAM")} · ${formatResourceAmount(resources.disk_gb, "GB disk")}`;
  $("budget-resources-assigned").textContent =
    `Resources assigned: ${formatResourceAmount(quota.cpu_core, "CPU")} · ${formatResourceAmount(quota.ram_gb, "GB RAM")} · ${formatResourceAmount(quota.disk_gb, "GB disk")}`;
  $("budget-error").hidden = true;
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
    await loadEnvironments();
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

function renderEnvironments(envs) {
  $("total-count").textContent = envs.length;
  $("running-count").textContent = envs.filter(
    (e) => e.status === "RUNNING",
  ).length;
  $("provisioning-count").textContent = envs.filter(
    (e) => e.status === "PROVISIONING",
  ).length;
  const grid = $("environment-grid");
  grid.replaceChildren();
  for (const env of envs) {
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
  grid.hidden = envs.length === 0;
  $("list-status").hidden = envs.length > 0;
  $("list-status").className = "list-status";
  $("list-status").textContent = envs.length
    ? ""
    : "No environments yet. Environments created with this account will appear here.";
}

async function loadEnvironments() {
  $("refresh").disabled = true;
  $("list-status").hidden = false;
  $("list-status").className = "list-status";
  $("list-status").textContent = "Loading environments…";
  try {
    renderEnvironments(await listEnvironments());
    await loadBudget();
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
    const envs = await listEnvironments();
    sessionStorage.setItem(keyStorage, apiKey);
    $("api-key").value = "";
    showDashboard();
    renderEnvironments(envs);
    await loadBudget();
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

$("refresh").addEventListener("click", loadEnvironments);
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
