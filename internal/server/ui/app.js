/* Weftly SPA — vanilla ES modules, no build step.
 *
 * State model:
 *   - hash-based routing: #/, #/workflow/<id>, #/runs/<id>, #/history
 *   - localStorage['weftly.token'] holds the bearer token; user prompted
 *     on first API 401.
 *   - each view calls into api() then renders into #app via <template>
 *     cloning; there is no framework — we lean on the DOM.
 */

const TOKEN_KEY = "weftly.token";
const $ = (sel, root = document) => root.querySelector(sel);
const el = (tmplId) => document.getElementById(tmplId).content.firstElementChild.cloneNode(true);

// --------------- auth + fetch wrapper -------------------

function getToken() {
  return localStorage.getItem(TOKEN_KEY) || "";
}
function setToken(v) {
  if (v) localStorage.setItem(TOKEN_KEY, v);
  else localStorage.removeItem(TOKEN_KEY);
}
async function api(path, opts = {}) {
  const headers = new Headers(opts.headers || {});
  const t = getToken();
  if (t) headers.set("Authorization", "Bearer " + t);
  if (opts.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  const res = await fetch(path, { ...opts, headers });
  if (res.status === 401) {
    const tok = prompt("API bearer token:", t || "");
    if (tok) {
      setToken(tok);
      return api(path, opts);
    }
    throw new Error("Unauthorized");
  }
  return res;
}

// --------------- shell + routing -----------------------

function renderShell() {
  const app = $("#app");
  app.innerHTML = "";
  const shell = el("tmpl-shell");
  app.appendChild(shell);
  // highlight nav
  const hash = window.location.hash || "#/";
  const nav = hash.startsWith("#/history")
    ? "history"
    : hash.startsWith("#/schedules")
      ? "schedules"
      : "catalogue";
  shell.querySelectorAll(".wf-nav a").forEach((a) => {
    if (a.dataset.nav === nav) a.setAttribute("aria-current", "page");
  });
  return shell.querySelector(".wf-main");
}

function route() {
  const hash = window.location.hash || "#/";
  if (hash === "#/" || hash === "") return renderCatalogue();
  if (hash.startsWith("#/workflow/")) return renderForm(hash.slice("#/workflow/".length));
  if (hash.startsWith("#/runs/")) return renderRun(hash.slice("#/runs/".length));
  if (hash === "#/history") return renderHistory();
  if (hash === "#/schedules") return renderSchedules();
  renderCatalogue();
}
window.addEventListener("hashchange", route);

// --------------- catalogue view ------------------------

async function renderCatalogue() {
  const main = renderShell();
  const page = el("tmpl-catalogue");
  main.appendChild(page);
  let workflows = [];
  try {
    const r = await api("/workflows");
    if (!r.ok) throw new Error(await r.text());
    workflows = (await r.json()).workflows || [];
  } catch (e) {
    page.querySelector('[data-role="grid"]').textContent = "Error loading workflows: " + e.message;
    return;
  }
  const grid = page.querySelector('[data-role="grid"]');
  const search = page.querySelector('[data-role="search"]');
  const draw = (term) => {
    grid.innerHTML = "";
    const t = (term || "").toLowerCase();
    for (const w of workflows) {
      if (t && !(w.id + " " + w.name + " " + (w.description || "")).toLowerCase().includes(t)) continue;
      const card = el("tmpl-workflow-card");
      card.setAttribute("href", "#/workflow/" + encodeURIComponent(w.id));
      card.querySelector('[data-role="title"]').textContent = w.name || w.id;
      card.querySelector('[data-role="desc"]').textContent = w.description || "";
      const inputs = Object.keys(w.inputs || {});
      card.querySelector('[data-role="steps"]').textContent =
        inputs.length ? inputs.length + " input" + (inputs.length > 1 ? "s" : "") : "no inputs";
      grid.appendChild(card);
    }
    if (!grid.children.length) grid.textContent = "No workflows match.";
  };
  search.addEventListener("input", (e) => draw(e.target.value));
  draw("");
}

// --------------- form view -----------------------------

async function renderForm(id) {
  const main = renderShell();
  const page = el("tmpl-form");
  main.appendChild(page);
  const r = await api("/workflows/" + encodeURIComponent(id));
  if (!r.ok) {
    page.querySelector('[data-role="fields"]').textContent = "Workflow not found.";
    return;
  }
  const wf = await r.json();
  page.querySelector('[data-role="title"]').textContent = wf.name || wf.id;
  page.querySelector('[data-role="desc"]').textContent = wf.description || "";

  const fields = page.querySelector('[data-role="fields"]');
  const inputs = wf.inputs || {};
  const entries = Object.entries(inputs);
  const values = {};
  for (const [name, spec] of entries) {
    const wrap = document.createElement("div");
    wrap.className = "wf-field";
    // A long description or an enum picklist earns a full-row slot so
    // the help text wraps and the select isn't squeezed.
    if ((spec.description && spec.description.length > 60) || (spec.enum && spec.enum.length > 4)) {
      wrap.classList.add("full");
    }

    const label = document.createElement("label");
    label.className = "wf-label";
    label.textContent = name;
    if (spec.required) {
      const req = document.createElement("span");
      req.className = "wf-req";
      req.textContent = " *";
      req.title = "required";
      label.appendChild(req);
    }
    if (spec.secret) {
      const badge = document.createElement("span");
      badge.className = "wf-badge-inline";
      badge.textContent = "secret";
      badge.title = "value is masked in logs and state";
      label.appendChild(badge);
    }
    if (spec.type && spec.type !== "string") {
      const badge = document.createElement("span");
      badge.className = "wf-badge-inline dim";
      badge.textContent = spec.type;
      label.appendChild(badge);
    }
    wrap.appendChild(label);

    let input;
    if (Array.isArray(spec.enum) && spec.enum.length > 0) {
      // Enum → picklist. Preserve the declared order; include a
      // blank "(default)" only when the input isn't required and no
      // explicit default was set.
      input = document.createElement("select");
      input.className = "wf-select";
      const showBlank = !spec.required && (spec.default === undefined || spec.default === null);
      if (showBlank) {
        const o = document.createElement("option");
        o.value = "";
        o.textContent = "(default)";
        input.appendChild(o);
      }
      for (const v of spec.enum) {
        const o = document.createElement("option");
        o.value = String(v);
        o.textContent = String(v);
        input.appendChild(o);
      }
    } else if (spec.type === "bool") {
      input = document.createElement("select");
      input.className = "wf-select";
      for (const opt of ["", "true", "false"]) {
        const o = document.createElement("option");
        o.value = opt;
        o.textContent = opt || "(default)";
        input.appendChild(o);
      }
    } else if (spec.type === "number") {
      input = document.createElement("input");
      input.type = "number";
      input.className = "wf-input";
    } else {
      input = document.createElement("input");
      input.type = spec.secret ? "password" : "text";
      input.className = "wf-input";
      // Show the default as a placeholder for secrets so the actual
      // value stays out of the DOM tree (screen shares, extensions).
      if (spec.secret && spec.default) {
        input.placeholder = "•••••• (default)";
      }
    }
    // Pre-populate the default (for non-secret inputs) so the user
    // sees exactly what will be sent. For secrets we only carry it in
    // `values` — the input itself stays blank — so the value never
    // renders visibly.
    if (spec.default !== undefined && spec.default !== null) {
      values[name] = spec.default;
      if (!spec.secret) {
        input.value = String(spec.default);
      }
    }
    input.addEventListener("input", (e) => (values[name] = e.target.value));
    input.addEventListener("change", (e) => (values[name] = e.target.value));
    wrap.appendChild(input);
    if (spec.description) {
      const help = document.createElement("div");
      help.className = "wf-help";
      help.textContent = spec.description;
      wrap.appendChild(help);
    }
    fields.appendChild(wrap);
  }

  // Recent-runs strip below the form.
  try {
    const rr = await api("/runs?workflow=" + encodeURIComponent(id));
    if (rr.ok) {
      const { runs } = await rr.json();
      if (runs && runs.length) {
        const strip = document.createElement("div");
        strip.style.marginTop = "24px";
        strip.style.borderTop = "1px solid var(--border)";
        strip.style.paddingTop = "16px";
        const label = document.createElement("div");
        label.className = "wf-sub";
        label.style.marginBottom = "8px";
        label.textContent = "Recent runs of this workflow";
        strip.appendChild(label);
        for (const run of runs.slice(0, 5)) {
          const a = document.createElement("a");
          a.href = "#/runs/" + encodeURIComponent(run.run_id);
          a.className = "wf-history-item";
          a.style.marginTop = "6px";
          const b = document.createElement("span");
          b.className = "wf-badge " + (run.status || "pending");
          b.textContent = run.status || "pending";
          const t = document.createElement("span");
          t.className = "id";
          t.textContent = run.run_id;
          const when = document.createElement("span");
          when.className = "id";
          when.style.marginLeft = "auto";
          when.textContent = new Date(run.started_at).toLocaleString();
          a.appendChild(b);
          a.appendChild(t);
          a.appendChild(when);
          strip.appendChild(a);
        }
        page.appendChild(strip);
      }
    }
  } catch (_) {}

  const status = page.querySelector('[data-role="status"]');
  const submit = page.querySelector('[data-role="submit"]');
  submit.addEventListener("click", async () => {
    submit.disabled = true;
    status.textContent = "Starting…";
    try {
      const r = await api("/runs", {
        method: "POST",
        body: JSON.stringify({ workflow: id, inputs: coerceInputs(inputs, values) }),
      });
      if (!r.ok) throw new Error(await r.text());
      const { run_id } = await r.json();
      window.location.hash = "#/runs/" + encodeURIComponent(run_id);
    } catch (e) {
      status.textContent = "Error: " + e.message;
      submit.disabled = false;
    }
  });
}

function coerceInputs(schema, values) {
  const out = {};
  for (const [k, v] of Object.entries(values)) {
    if (v === "" || v === undefined) continue;
    const spec = schema[k] || {};
    if (spec.type === "number") out[k] = Number(v);
    else if (spec.type === "bool") out[k] = v === "true";
    else out[k] = String(v);
  }
  return out;
}

// --------------- live run view -------------------------

async function renderRun(runID) {
  const main = renderShell();
  const page = el("tmpl-live");
  main.appendChild(page);
  page.querySelector('[data-role="title"]').textContent = "Run";
  page.querySelector('[data-role="runid"]').textContent = runID;
  const badge = page.querySelector('[data-role="badge"]');
  const stepsEl = page.querySelector('[data-role="steps"]');
  const summaryEl = page.querySelector('[data-role="summary"]');
  const summaryBody = page.querySelector('[data-role="summary-body"]');
  const artifactsEl = page.querySelector('[data-role="artifacts"]');
  const artifactList = page.querySelector('[data-role="artifact-list"]');
  const conn = page.querySelector('[data-role="conn"]');
  const cancelBtn = page.querySelector('[data-role="cancel"]');

  badge.textContent = "running";
  badge.className = "wf-badge running";
  cancelBtn.hidden = false;
  cancelBtn.addEventListener("click", async () => {
    if (!confirm("Cancel this run? Any in-flight step will be killed.")) return;
    cancelBtn.disabled = true;
    cancelBtn.textContent = "Cancelling…";
    try {
      const r = await api("/runs/" + encodeURIComponent(runID), { method: "DELETE" });
      if (!r.ok) throw new Error(await r.text());
    } catch (e) {
      cancelBtn.disabled = false;
      cancelBtn.textContent = "Cancel run";
      alert("Cancel failed: " + e.message);
    }
  });

  const stepMap = new Map();
  const ensureStep = (id, name, action) => {
    if (stepMap.has(id)) return stepMap.get(id);
    const li = document.createElement("li");
    li.className = "wf-step";
    li.innerHTML =
      '<div class="wf-step-header">' +
        '<span class="wf-step-glyph">•</span>' +
        '<span class="wf-step-name"></span>' +
        '<span class="wf-step-meta"></span>' +
      '</div>' +
      '<div class="wf-step-logs" hidden></div>';
    li.querySelector(".wf-step-name").textContent = name || id;
    li.querySelector(".wf-step-meta").textContent = action || "";
    const logs = li.querySelector(".wf-step-logs");
    li.querySelector(".wf-step-header").addEventListener("click", () => {
      logs.hidden = !logs.hidden;
    });
    stepsEl.appendChild(li);
    const rec = { li, logs, glyph: li.querySelector(".wf-step-glyph"), meta: li.querySelector(".wf-step-meta") };
    stepMap.set(id, rec);
    return rec;
  };

  const glyphs = {
    running: '<span style="color:var(--accent-2)">◐</span>',
    success: '<span style="color:var(--ok)">✓</span>',
    failed: '<span style="color:var(--err)">✗</span>',
    "timed-out": '<span style="color:var(--err)">✗</span>',
    "failed-continued": '<span style="color:var(--warn)">⚠</span>',
    skipped: '<span style="color:var(--fg-faint)">⊘</span>',
  };

  const handle = (e) => {
    const ev = e.event || e;
    switch (e.type || e.event?.type) {
      case "RunStarted":
        page.querySelector('[data-role="title"]').textContent = ev.Workflow || "Run";
        break;
      case "StepStarted": {
        const s = ensureStep(ev.StepID, ev.Name, ev.Action);
        s.glyph.innerHTML = glyphs.running;
        break;
      }
      case "StepLog": {
        const s = ensureStep(ev.StepID);
        s.logs.hidden = false;
        const line = document.createElement("div");
        line.className =
          ev.Stream === "stderr" ? "stderr" : ev.Stream === "info" ? "info" : "out-line";
        line.textContent = ev.Line || "";
        s.logs.appendChild(line);
        s.logs.scrollTop = s.logs.scrollHeight;
        break;
      }
      case "StepOutput": {
        const s = ensureStep(ev.StepID);
        s.logs.hidden = false;
        const line = document.createElement("div");
        line.className = "info";
        line.textContent = "→ " + ev.Key + "=" + (typeof ev.Value === "string" ? ev.Value : JSON.stringify(ev.Value));
        s.logs.appendChild(line);
        break;
      }
      case "StepRetry": {
        const s = ensureStep(ev.StepID);
        s.logs.hidden = false;
        const line = document.createElement("div");
        line.className = "info";
        const delay = Math.round((ev.Delay || 0) / 1e6);
        let text = `↻ retrying (attempt ${ev.Attempt + 1}/${ev.Of} in ${delay}ms) — ${ev.Cause}`;
        if (ev.Err) text += `: ${ev.Err}`;
        line.textContent = text;
        s.logs.appendChild(line);
        s.glyph.innerHTML = glyphs.running;
        break;
      }
      case "StepFinished": {
        const s = ensureStep(ev.StepID);
        s.glyph.innerHTML = glyphs[ev.Status] || "•";
        const dur = ev.Duration ? Math.round(ev.Duration / 1e6) + "ms" : "";
        s.meta.textContent = (ev.Resumed ? "(resumed) " : "") + ev.Status + (dur ? " · " + dur : "");
        // If the step failed, surface the action's error inline in the
        // logs pane in red. Without this the SPA silently drops
        // ev.Err — the operator sees 'failed · 1ms' but can't tell
        // why (e.g. 'unsupported protocol scheme' from a bad URL).
        if (ev.Err) {
          s.logs.hidden = false;
          const line = document.createElement("div");
          line.className = "stderr";
          line.textContent = "✗ " + ev.Err;
          s.logs.appendChild(line);
        }
        break;
      }
      case "SummaryEmitted": {
        summaryEl.hidden = false;
        const div = document.createElement("div");
        div.textContent = ev.Markdown || "";
        div.style.whiteSpace = "pre-wrap";
        summaryBody.appendChild(div);
        break;
      }
      case "ArtifactUploaded": {
        artifactsEl.hidden = false;
        const li = document.createElement("li");
        const a = document.createElement("a");
        a.href = "/runs/" + encodeURIComponent(runID) + "/artifacts/" + encodeURIComponent(basename(ev.Path));
        a.textContent = ev.Name || basename(ev.Path);
        li.appendChild(a);
        li.appendChild(document.createTextNode(" (" + ev.Size + " bytes)"));
        artifactList.appendChild(li);
        break;
      }
      case "RunFinished": {
        const dur = ev.Duration ? Math.round(ev.Duration / 1e6) + "ms" : "";
        badge.textContent = ev.Status + (dur ? " · " + dur : "");
        badge.className = "wf-badge " + ev.Status;
        cancelBtn.hidden = true;
      }
        // Close the EventSource so the browser stops reconnecting
        // every ~3s to a completed run — otherwise the server keeps
        // getting GET /runs/{id}/events forever from an idle tab.
        // Delay slightly so any straggler frames (heartbeat, close
        // frame) drain first.
        setTimeout(() => src.close(), 250);
        break;
    }
  };

  // Auth: EventSource can't set headers, so we pass the token as a query
  // string. The server middleware also accepts ?token= as an alternative
  // to the Authorization header (see server/middleware.go).
  const t = getToken();
  const src = new EventSource("/runs/" + encodeURIComponent(runID) + "/events" + (t ? "?token=" + encodeURIComponent(t) : ""));
  src.onmessage = (ev) => {
    try { handle(JSON.parse(ev.data)); } catch (_) {}
  };
  src.onopen = () => { conn.hidden = true; };
  src.onerror = () => {
    // A completed run's server-closed stream also fires onerror; if
    // we've already recorded a terminal status, don't show the
    // "Reconnecting…" banner — the source is about to be closed.
    if (badge.textContent && badge.textContent !== "running") return;
    conn.hidden = false;
  };
}

function basename(p) {
  return (p || "").split("/").pop();
}

// --------------- history view --------------------------

async function renderHistory() {
  const main = renderShell();
  const page = el("tmpl-history");
  main.appendChild(page);
  const list = page.querySelector('[data-role="list"]');
  try {
    const r = await api("/runs");
    if (!r.ok) throw new Error(await r.text());
    const { runs } = await r.json();
    if (!runs || !runs.length) {
      const empty = document.createElement("div");
      empty.className = "wf-sub";
      empty.textContent = "No runs yet — pick a runbook from the Catalogue.";
      list.appendChild(empty);
      return;
    }
    for (const run of runs) {
      const row = document.createElement("a");
      row.href = "#/runs/" + encodeURIComponent(run.run_id);
      row.className = "wf-history-item";
      const status = document.createElement("span");
      status.className = "wf-badge " + (run.status || "pending");
      status.textContent = run.status || "pending";
      const wf = document.createElement("span");
      wf.style.fontWeight = "500";
      wf.textContent = run.workflow || "(unknown)";
      const id = document.createElement("span");
      id.className = "id";
      id.textContent = run.run_id;
      const when = document.createElement("span");
      when.className = "id";
      when.style.marginLeft = "auto";
      when.textContent = new Date(run.started_at).toLocaleString();
      row.appendChild(status);
      row.appendChild(wf);
      row.appendChild(id);
      row.appendChild(when);
      list.appendChild(row);
    }
  } catch (e) {
    list.textContent = "Error loading history: " + e.message;
  }
}

// --------------- schedules view ------------------------

async function renderSchedules() {
  const main = renderShell();
  const page = el("tmpl-schedules");
  main.appendChild(page);
  const list = page.querySelector('[data-role="list"]');
  try {
    const r = await api("/schedules");
    if (!r.ok) throw new Error(await r.text());
    const { schedules } = await r.json();
    if (!schedules || !schedules.length) {
      const empty = document.createElement("div");
      empty.className = "wf-sub";
      empty.textContent =
        "No schedules configured. Point `weftly server --schedules schedules.yaml` at a file to enable cron-driven runs.";
      list.appendChild(empty);
      return;
    }
    for (const s of schedules) {
      const row = document.createElement("div");
      row.className = "wf-history-item wf-schedule-row";
      const id = document.createElement("span");
      id.style.fontWeight = "500";
      id.textContent = s.id;
      const wf = document.createElement("span");
      wf.className = "id";
      wf.textContent = s.workflow;
      const cron = document.createElement("span");
      cron.className = "cron";
      cron.textContent = s.cron;
      const next = document.createElement("span");
      next.className = "id";
      next.style.marginLeft = "12px";
      next.textContent = s.next_fire
        ? "next: " + new Date(s.next_fire).toLocaleString()
        : s.parse_error
          ? "parse error"
          : "";
      const trigger = document.createElement("button");
      trigger.className = "wf-btn wf-btn-primary trigger";
      trigger.textContent = "Trigger now";
      trigger.addEventListener("click", async () => {
        trigger.disabled = true;
        trigger.textContent = "Starting…";
        try {
          const rr = await api("/schedules/" + encodeURIComponent(s.id) + "/trigger", { method: "POST" });
          if (!rr.ok) throw new Error(await rr.text());
          const { run_id } = await rr.json();
          window.location.hash = "#/runs/" + encodeURIComponent(run_id);
        } catch (e) {
          trigger.disabled = false;
          trigger.textContent = "Trigger now";
          alert("Trigger failed: " + e.message);
        }
      });
      row.appendChild(id);
      row.appendChild(wf);
      row.appendChild(cron);
      row.appendChild(next);
      row.appendChild(trigger);
      list.appendChild(row);
      if (s.last_result && s.last_result.error) {
        const err = document.createElement("div");
        err.className = "wf-sub";
        err.style.color = "var(--err)";
        err.style.paddingLeft = "12px";
        err.textContent = "last fire: " + s.last_result.error;
        list.appendChild(err);
      }
    }
  } catch (e) {
    list.textContent = "Error loading schedules: " + e.message;
  }
}

// bootstrap
route();
