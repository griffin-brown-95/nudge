// Live results dashboard. Reads GET /stats (computed and cached by the API)
// and renders it. Results below the API's minimum sample arrive without a
// rate, and are drawn as "collecting data".
import { API_BASE } from "../config.js";

const REFRESH_MS = 60_000;
const $ = (id) => document.getElementById(id);

const NAMES = {
  frame: { tiles: "Tiles", split: "Split screen" },
  layout: { side_by_side: "side by side", stacked: "stacked" },
  attribute: { color: "Color", emphasis: "Fill style", size: "Size", frame: "Frame", labels: "Labels",
    label_order: "Label order", default_focus: "Pre-selected", prime: "Prime" },
  level: {
    base: "Base size", plus8: "+8% larger", solid: "Solid", soft: "Soft", outline: "Outline",
    letters: "A / B", numbers: "1 / 2", none: "None", normal: "Normal", reversed: "Reversed",
    first: "First", second: "Second", gut: "“Gut”", careful: "“Carefully”",
    tiles: "Tiles", split: "Split screen",
  },
  input: { mouse: "Mouse", touch: "Touch", pen: "Pen", keyboard: "Keyboard" },
};
const HUES = { red: 25, orange: 60, green: 145, blue: 250, purple: 305 };

const level = (l) => NAMES.level[l] || l.charAt(0).toUpperCase() + l.slice(1);
const pct = (x, digits = 0) => `${(x * 100).toFixed(digits)}%`;
const int = (n) => n.toLocaleString("en-US");
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);

let snap = null;
let frameFilter = "";
let fetchedAt = 0;

// ---- Data ----------------------------------------------------------------

async function load() {
  try {
    const res = await fetch(`${API_BASE}/stats`, { cache: "no-store" });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    snap = await res.json();
    fetchedAt = Date.now();
    render();
  } catch (err) {
    console.warn("dashboard: stats unavailable", err);
    $("status").textContent = snap
      ? "Couldn't refresh; showing the last results. Retrying shortly."
      : "The results service is waking up or unavailable. Retrying shortly.";
  }
}

function tickStatus() {
  if (!snap) return;
  const secs = Math.round((Date.now() - new Date(snap.generated_at).getTime()) / 1000);
  const ago = secs < 60 ? `${Math.max(secs, 0)}s ago` : `${Math.round(secs / 60)} min ago`;
  $("status").textContent = `Experiment version ${snap.version} · updated ${ago} · refreshes every minute`;
}

// ---- Render --------------------------------------------------------------

function render() {
  $("min-n").textContent = snap.min_n;
  renderKpis();
  renderDaily();
  renderPosition();
  renderAttributes();
  renderMatchups();
  renderDecisions();
  renderBalance();
  tickStatus();
}

function renderKpis() {
  const p = snap.progress;
  const tiles = [
    ["Visitors", int(p.visitors), `${int(p.sessions)} sessions`],
    ["Choices made", int(p.choices), `${int(p.choices_last_24h)} in the last 24h`],
    ["Trials shown", int(p.trials_shown), "each one randomized"],
    ["Left unanswered", p.abandonment == null ? "–" : pct(p.abandonment, 1), "trials shown 10+ min ago"],
  ];
  $("kpis").innerHTML = tiles.map(([label, value, sub]) => `
    <div class="kpi">
      <p class="kpi-label">${label}</p>
      <p class="kpi-value">${value}</p>
      <p class="kpi-sub">${sub}</p>
    </div>`).join("");
}

function renderDaily() {
  const days = snap.daily || [];
  const W = 600, H = 160, top = 14, bottom = 20, gap = 2;
  const max = Math.max(1, ...days.map((d) => d.choices));
  const plotH = H - top - bottom;
  const bw = W / Math.max(days.length, 1);
  const y = (v) => top + plotH - (v / max) * plotH;
  const short = (d) => new Date(`${d}T00:00:00Z`).toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });

  const bars = days.map((d, i) => {
    const h = Math.max(0, (d.choices / max) * plotH);
    const x = i * bw + gap / 2;
    const w = Math.max(1, bw - gap);
    const r = Math.min(4, w / 2, h);
    // Rounded top only: the data-end gets the radius, the baseline stays square.
    const path = h <= 0 ? "" :
      `M${x},${top + plotH} V${y(d.choices) + r} Q${x},${y(d.choices)} ${x + r},${y(d.choices)} ` +
      `H${x + w - r} Q${x + w},${y(d.choices)} ${x + w},${y(d.choices) + r} V${top + plotH} Z`;
    const tip = `<b>${short(d.date)}</b><br>${int(d.choices)} choices · ${int(d.visitors)} visitors`;
    return `<rect class="bar-hit" x="${i * bw}" y="${top}" width="${bw}" height="${plotH}"
              tabindex="0" data-tip="${esc(tip)}" aria-label="${short(d.date)}: ${d.choices} choices"></rect>
            <path class="bar" d="${path}"></path>`;
  }).join("");

  $("daily").innerHTML = `
    <svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" role="img"
         aria-label="Choices per day for the last 30 days, peak ${int(max)}">
      <line class="gridline" x1="0" x2="${W}" y1="${top}" y2="${top}"></line>
      <text x="0" y="${top - 4}">${int(max)}</text>
      ${bars}
      <line class="baseline" x1="0" x2="${W}" y1="${top + plotH}" y2="${top + plotH}"></line>
      ${days.length ? `<text x="0" y="${H - 4}">${short(days[0].date)}</text>
      <text x="${W}" y="${H - 4}" text-anchor="end">${short(days[days.length - 1].date)}</text>` : ""}
    </svg>`;
}

// An interval chart: one row per rate, dot = share, bar = 95% range, dashed
// line = 50%. All rows in one chart share the same domain.
function intervalChart(rows, { title = "", what = "picked" } = {}) {
  const shown = rows.filter((r) => r.share != null);
  let lo = Math.min(0.35, ...shown.map((r) => r.lo - 0.02));
  let hi = Math.max(0.65, ...shown.map((r) => r.hi + 0.02));
  lo = Math.max(0, Math.floor(lo * 10) / 10);
  hi = Math.min(1, Math.ceil(hi * 10) / 10);
  const x = (v) => `${((v - lo) / (hi - lo)) * 100}%`;
  const ticks = [];
  for (let t = lo; t <= hi + 1e-9; t += 0.1) ticks.push(Math.round(t * 10) / 10);

  const body = rows.map((r) => {
    const label = `<div class="row-label${r.total ? " total" : ""}">${r.swatch || ""}<span>${esc(r.label)}</span></div>`;
    if (r.share == null) {
      return `<div class="row" tabindex="0" data-tip="${esc(`<b>${r.label}</b><br>${int(r.n)} of ${snap.min_n} trials needed`)}">
        ${label}
        <div class="track"><div class="pending">collecting data</div></div>
        <div class="row-value">n = ${int(r.n)} of ${snap.min_n}</div>
      </div>`;
    }
    const tip = `<b>${r.label}</b><br>${what} ${int(r.hits)} of ${int(r.n)} times<br>` +
      `${pct(r.share, 1)} (95% range ${pct(r.lo, 1)}–${pct(r.hi, 1)})`;
    return `<div class="row" tabindex="0" data-tip="${esc(tip)}">
      ${label}
      <div class="track" aria-hidden="true">
        <div class="ref" style="left:${x(0.5)}"></div>
        <div class="ci" style="left:${x(r.lo)};width:calc(${x(r.hi)} - ${x(r.lo)})"></div>
        <div class="dot" style="left:${x(r.share)}"></div>
      </div>
      <div class="row-value"><b>${pct(r.share)}</b> ${pct(r.lo)}–${pct(r.hi)} · n=${int(r.n)}</div>
    </div>`;
  }).join("");

  return `<div class="interval">
    ${title ? `<p class="interval-title">${esc(title)}</p>` : ""}
    ${body}
    <div class="axis" aria-hidden="true"><span></span>
      <div class="ticks">${ticks.map((t) => `<span style="left:${x(t)}">${pct(t)}</span>`).join("")}</div><span></span>
    </div>
  </div>`;
}

function renderPosition() {
  const rows = (snap.position || []).map((p) => {
    let label = "All picks";
    if (p.frame && p.layout) label = `${NAMES.frame[p.frame] || p.frame}, ${NAMES.layout[p.layout] || p.layout}`;
    else if (p.frame) label = `${NAMES.frame[p.frame] || p.frame}`;
    return { ...p, label, total: !p.frame || !p.layout };
  });
  $("position").innerHTML = rows.length
    ? intervalChart(rows, { what: "First option picked" })
    : `<p class="note">No picks yet.</p>`;
}

function swatch(attr, lvl) {
  if (attr !== "color") return "";
  if (lvl === "gray") return `<i class="swatch gray" aria-hidden="true"></i>`;
  return `<i class="swatch" style="--h:${HUES[lvl] ?? 0}" aria-hidden="true"></i>`;
}

function renderAttributes() {
  const rows = (snap.attributes || []).filter((a) => (a.frame || "") === frameFilter);
  const groups = ["color", "emphasis", "size"].map((attr) => {
    const g = rows.filter((r) => r.attribute === attr)
      .map((r) => ({ ...r, label: level(r.level), swatch: swatch(attr, r.level) }))
      .sort((a, b) => (b.share ?? -1) - (a.share ?? -1));
    return g.length ? intervalChart(g, { title: NAMES.attribute[attr], what: "Picked" }) : "";
  }).join("");
  $("attributes").innerHTML = groups || `<p class="note">No contested picks yet.</p>`;
}

function renderMatchups() {
  const all = snap.matchups || [];
  const ranked = all.filter((m) => m.rank != null).slice(0, 10);
  const pending = all.length - all.filter((m) => m.rank != null).length;
  const rows = ranked.map((m) => `
    <tr>
      <td class="num">${m.rank}</td>
      <td>${swatch(m.attribute, m.winner)} <b>${esc(level(m.winner))}</b> beats ${esc(level(m.loser))}</td>
      <td class="muted">${esc(NAMES.attribute[m.attribute] || m.attribute)}</td>
      <td class="num"><b>${pct(m.winner_share)}</b></td>
      <td class="num">at least ${pct(m.winner_share_lo)}</td>
      <td class="num">${int(m.n)}</td>
    </tr>`).join("");
  $("matchups").innerHTML = ranked.length ? `
    <div class="table-wrap"><table>
      <thead><tr><th class="num">#</th><th>Matchup</th><th>Factor</th><th class="num">Winner's share</th>
        <th class="num">95% lower bound</th><th class="num">Trials</th></tr></thead>
      <tbody>${rows}</tbody>
    </table></div>
    ${pending ? `<p class="note">${pending} more matchup${pending === 1 ? "" : "s"} still collecting data.</p>` : ""}`
    : `<p class="note">No matchup has ${snap.min_n} trials yet. ${all.length ? `${all.length} are collecting data.` : ""}</p>`;
}

function renderDecisions() {
  const rows = (snap.decisions || []).filter((d) => d.input || !d.frame);
  const cell = (v, f) => (v == null ? `<td class="num muted">–</td>` : `<td class="num">${f(v)}</td>`);
  const body = rows.map((d) => {
    const who = d.frame
      ? `${NAMES.frame[d.frame] || d.frame} · ${NAMES.input[d.input] || d.input}`
      : d.input ? `All frames · ${NAMES.input[d.input] || d.input}` : "<b>All picks</b>";
    if (d.median_latency_ms == null) {
      return `<tr><td>${who}</td><td class="num muted" colspan="4">collecting data (n = ${int(d.n)} of ${snap.min_n})</td></tr>`;
    }
    const touch = d.input === "touch";
    return `<tr><td>${who}</td>
      ${cell(d.median_latency_ms, (v) => `${(v / 1000).toFixed(2)} s`)}
      ${touch ? `<td class="num muted">n/a</td>`.repeat(3) : cell(d.considered_both_share, pct) + cell(d.changed_mind_share, pct) + cell(d.first_considered_wins, pct)}
      </tr>`;
  }).join("");
  $("decisions").innerHTML = rows.length ? `
    <div class="table-wrap"><table>
      <thead><tr><th></th><th class="num">Median time to pick</th><th class="num">Looked at both</th>
        <th class="num">Changed their mind</th><th class="num">First look wins</th></tr></thead>
      <tbody>${body}</tbody>
    </table></div>` : `<p class="note">No picks yet.</p>`;
}

function renderBalance() {
  $("balance").innerHTML = (snap.balance || []).map((b) => {
    const state = b.ok == null ? "pending" : b.ok ? "good" : "bad";
    const badge = {
      pending: `<span class="badge pending"><span class="icon" aria-hidden="true">…</span>Collecting</span>`,
      good: `<span class="badge good"><span class="icon" aria-hidden="true">✓</span>Balanced</span>`,
      bad: `<span class="badge bad"><span class="icon" aria-hidden="true">!</span>Mismatch</span>`,
    }[state];
    const levels = b.levels.map((l) => `${esc(level(l.level))} ${b.total ? pct(l.n / b.total) : "–"}`).join(" · ");
    const p = b.p == null ? "" : ` · p = ${b.p < 0.001 ? "<0.001" : b.p.toFixed(2)}`;
    return `<div class="factor">
      <div class="factor-head"><span class="factor-name">${esc(NAMES.attribute[b.factor] || b.factor)}</span>${badge}</div>
      <p class="factor-levels">${levels}</p>
      <p class="factor-levels">${int(b.total)} assignments${p}</p>
    </div>`;
  }).join("");
}

// ---- Interaction ---------------------------------------------------------

$("frame-filter").addEventListener("click", (e) => {
  const btn = e.target.closest("button[data-frame]");
  if (!btn) return;
  frameFilter = btn.dataset.frame;
  for (const b of $("frame-filter").querySelectorAll("button")) {
    b.setAttribute("aria-checked", String(b === btn));
  }
  if (snap) renderAttributes();
});

// One shared tooltip for every mark with data-tip (hover and keyboard focus).
const tooltip = $("tooltip");
function showTip(el, x, y) {
  tooltip.innerHTML = el.dataset.tip;
  tooltip.hidden = false;
  const r = tooltip.getBoundingClientRect();
  tooltip.style.left = `${Math.min(innerWidth - r.width - 8, Math.max(8, x + 12))}px`;
  tooltip.style.top = `${Math.max(8, y - r.height - 12)}px`;
}
document.addEventListener("pointermove", (e) => {
  const el = e.target.closest?.("[data-tip]");
  if (el) showTip(el, e.clientX, e.clientY);
  else tooltip.hidden = true;
});
document.addEventListener("focusin", (e) => {
  const el = e.target.closest?.("[data-tip]");
  if (!el) return;
  const r = el.getBoundingClientRect();
  showTip(el, r.left + r.width / 2, r.top);
});
document.addEventListener("focusout", () => (tooltip.hidden = true));
addEventListener("scroll", () => (tooltip.hidden = true), { passive: true });

load();
setInterval(load, REFRESH_MS);
setInterval(tickStatus, 10_000);
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible" && Date.now() - fetchedAt > REFRESH_MS) load();
});
