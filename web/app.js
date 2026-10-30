import { API_BASE, GAP_MS, TRIALS_TIMEOUT_MS } from "./config.js";
import { context, measureLayout, pointerType } from "./context.js";
import { enqueue, flush } from "./queue.js";
import { storage, uuid } from "./storage.js";
import { startTrace } from "./trace.js";

const $ = (id) => document.getElementById(id);
const app = $("app");
const buttons = [$("opt1"), $("opt2")];

const PRIMES = {
  gut: "Go with your gut.",
  careful: "Think carefully before choosing.",
};
const LABELS = { letters: ["A", "B"], numbers: ["1", "2"], none: ["", ""] };

// Used only if the API can't be reached: the visitor still gets to play,
// but nothing is logged because the server can't vouch for these trials.
const OFFLINE_LEVELS = {
  options: {
    color: ["red", "orange", "green", "blue", "purple", "gray"],
    emphasis: ["solid", "soft", "outline"],
    size: ["base", "plus8"],
  },
  trial: {
    frame: ["tiles", "split"],
    labels: ["letters", "numbers", "none"],
    label_order: ["normal", "reversed"],
    default_focus: ["none", "first", "second"],
  },
};

// ---- Persistent visitor state -------------------------------------------

const vid = storage.get("nudge.vid") || uuid();
storage.set("nudge.vid", vid);
const firstSeen = Number(storage.get("nudge.firstSeen")) || Date.now();
storage.set("nudge.firstSeen", String(firstSeen));

let progress = storage.getJSON("nudge.progress", { version: null, next: 0 });
let informed = storage.get("nudge.informed") === "1";

// ---- Session state --------------------------------------------------------

const s = {
  version: null,
  blockSize: 10,
  visitor: {},
  offline: false,
  block: [], // trials from the current trial up to the end of its block
  sessionId: null,
};

let lastMouse = null; // last fine-pointer position, as viewport fractions
let lastMousePx = null;
addEventListener("pointermove", (e) => {
  if (e.pointerType === "mouse" || e.pointerType === "pen") {
    lastMouse = { x: frac(e.clientX / innerWidth), y: frac(e.clientY / innerHeight) };
    lastMousePx = { x: e.clientX, y: e.clientY };
  }
}, { passive: true });

// Hover highlighting stays off until the pointer moves after the options
// appear. Otherwise whichever option happens to be under a resting cursor
// lights up on its own: an accidental nudge.
let disarmHover = () => {};
function armHoverOnMove() {
  disarmHover();
  app.dataset.hover = "off";
  const start = lastMousePx;
  const onMove = (e) => {
    if (e.pointerType !== "mouse" && e.pointerType !== "pen") return;
    if (start && Math.hypot(e.clientX - start.x, e.clientY - start.y) < 4) return;
    app.dataset.hover = "on";
    disarmHover();
  };
  addEventListener("pointermove", onMove, { passive: true });
  disarmHover = () => removeEventListener("pointermove", onMove);
}

function frac(v) {
  return Math.min(1, Math.max(0, Math.round(v * 1000) / 1000));
}

// ---- API ------------------------------------------------------------------

async function fetchTrials(start, count) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), TRIALS_TIMEOUT_MS);
  try {
    const url = `${API_BASE}/trials?vid=${encodeURIComponent(vid)}&start=${start}&count=${count}`;
    const res = await fetch(url, { signal: ctrl.signal, cache: "no-store" });
    if (!res.ok) throw new Error(`trials: HTTP ${res.status}`);
    return await res.json();
  } finally {
    clearTimeout(timer);
  }
}

// Loads trials from progress.next to the end of that trial's block.
async function loadBlock() {
  const remaining = (next) => s.blockSize - (next % s.blockSize);
  try {
    let data = await fetchTrials(progress.next, remaining(progress.next));
    if (data.version !== progress.version) {
      // New experiment version: assignments reshuffled, start over.
      const restart = progress.version !== null && progress.next !== 0;
      progress = { version: data.version, next: 0 };
      storage.setJSON("nudge.progress", progress);
      if (restart) data = await fetchTrials(0, data.block_size);
    }
    Object.assign(s, {
      version: data.version,
      blockSize: data.block_size,
      visitor: data.visitor,
      block: data.trials,
      offline: false,
    });
  } catch (err) {
    console.warn("nudge: API unavailable, playing offline (not logged)", err);
    s.offline = true;
    s.block = offlineBlock(progress.next, remaining(progress.next));
  }
}

function offlineBlock(start, count) {
  const pick = (xs) => xs[Math.floor(Math.random() * xs.length)];
  const draw = (levels) => Object.fromEntries(Object.entries(levels).map(([k, v]) => [k, pick(v)]));
  return Array.from({ length: count }, (_, i) => ({
    index: start + i,
    block: Math.floor((start + i) / s.blockSize),
    trial: draw(OFFLINE_LEVELS.trial),
    options: [draw(OFFLINE_LEVELS.options), draw(OFFLINE_LEVELS.options)],
  }));
}

function send(event) {
  if (s.offline) return;
  enqueue({ ...event, visitor_id: vid, session_id: s.sessionId });
}

// ---- Screens --------------------------------------------------------------

function show(screen, main, sub = "") {
  app.dataset.screen = screen;
  $("prompt-main").textContent = main;
  $("prompt-sub").textContent = sub;
}

function primeText() {
  return PRIMES[s.visitor.prime] || "";
}

async function fontsReady() {
  try {
    await Promise.race([
      document.fonts.load('750 40px "Inter"').then(() => document.fonts.ready),
      new Promise((r) => setTimeout(r, 3000)),
    ]);
  } catch {
    /* render with the fallback font */
  }
  return document.fonts.check('750 40px "Inter"');
}

let fontsOK = false;

async function init() {
  show("start", "Just pick one.", "Loading…");
  const ready = $("ready");
  [fontsOK] = await Promise.all([fontsReady(), loadBlock()]);
  show("start", "Just pick one.", primeText() || "Quick choices. No right answers.");
  ready.textContent = "Are you ready?";
  ready.disabled = false;
  ready.focus({ preventScroll: true });
  flush(); // send anything left over from a previous visit
}

let readyPointer = null;
$("ready").addEventListener("pointerdown", (e) => (readyPointer = e.pointerType));
$("ready").addEventListener("click", (e) => {
  const sessions = (Number(storage.get("nudge.sessions")) || 0) + 1;
  storage.set("nudge.sessions", String(sessions));
  s.sessionId = uuid();

  const mouse = e.detail > 0 && (readyPointer === "mouse" || readyPointer === "pen");
  send({
    type: "session_start",
    session_number: sessions,
    days_since_first: Math.floor((Date.now() - firstSeen) / 86_400_000),
    fonts_ready: fontsOK,
    start_pointer: mouse ? { x: frac(e.clientX / innerWidth), y: frac(e.clientY / innerHeight) } : null,
    ctx: context(s.version, innerWidth >= 560 ? "side_by_side" : "stacked"),
  });
  runTrial(0);
});

// ---- Trials ---------------------------------------------------------------

function renderOptions(trial) {
  const order = trial.trial.label_order === "reversed" ? [1, 0] : [0, 1];
  const labels = LABELS[trial.trial.labels] || LABELS.none;
  buttons.forEach((btn, i) => {
    const o = trial.options[i];
    btn.dataset.color = o.color;
    btn.dataset.emphasis = o.emphasis;
    btn.dataset.size = o.size;
    const text = labels[order[i]];
    btn.textContent = text;
    btn.setAttribute("aria-label", text ? `Option ${text}` : i === 0 ? "First option" : "Second option");
    btn.classList.remove("is-focused");
    btn.disabled = false;
  });
}

// In the split frame the options fill the stage's band edge to edge, so the
// Ready button and the dot still sit exactly on the line between them.
function setSplitBounds() {
  const r = $("stage").getBoundingClientRect();
  app.style.setProperty("--split-top", `${Math.round(r.top)}px`);
  app.style.setProperty("--split-bottom", `${Math.round(innerHeight - r.bottom)}px`);
}
addEventListener("resize", setSplitBounds);

// The corner counter is the visitor's running total of picks. From pick 20
// on, every 10th pick shows a joke "rating" instead, escalating with
// dedication. It's a pure function of the pick number, so analysis can tell
// exactly which trials showed a joke from trial_index alone.
const COUNTER_JOKES = [
  [100, ["read a book / 10", "touch grass / 10", "go outside / 10", "legend / 10"]],
  [50, ["a lot / 10", "still here? / 10", "dedicated / 10"]],
  [20, ["wow / 10", "nice / 10", "impressive / 10"]],
];

function counterText(pick) {
  if (pick >= 20 && pick % 10 === 0) {
    const [min, jokes] = COUNTER_JOKES.find(([min]) => pick >= min);
    return jokes[((pick - min) / 10) % jokes.length];
  }
  return `Pick ${pick}`;
}

// The "Keep going?" check-in only follows a visitor's first block (picks
// 1–10, ever). After that, play flows straight through: the next block is
// fetched a few picks early so crossing a block boundary is just a normal gap.
let upcoming = null;

function fetchNextBlock() {
  const start = s.block[s.block.length - 1].index + 1;
  const count = s.blockSize - (start % s.blockSize);
  if (s.offline) return Promise.resolve(offlineBlock(start, count));
  return fetchTrials(start, count)
    .then((d) => {
      if (d.version !== s.version) throw new Error("experiment version changed");
      return d.trials;
    })
    .catch((err) => {
      console.warn("nudge: API unavailable, playing offline (not logged)", err);
      s.offline = true;
      return offlineBlock(start, count);
    });
}

function runTrial(i) {
  const trial = s.block[i];
  const renderStart = performance.now();
  $("progress").textContent = counterText(trial.index + 1);
  const last = s.block[s.block.length - 1];
  if (!upcoming && last.block !== 0 && i === Math.max(0, s.block.length - 3)) {
    upcoming = fetchNextBlock();
  }

  app.dataset.frame = trial.trial.frame || "tiles";
  setSplitBounds();
  armHoverOnMove();
  renderOptions(trial);
  show("trial", "Pick one.", primeText());

  const trace = startTrace(buttons);
  const focusSlot = { first: 0, second: 1 }[trial.trial.default_focus];
  if (focusSlot !== undefined) {
    const btn = buttons[focusSlot];
    trace.focusProgrammatically(btn);
    btn.classList.add("is-focused");
    btn.addEventListener("blur", () => btn.classList.remove("is-focused"), { once: true });
  } else {
    // Park focus on the stage so Tab moves to the options without picking one.
    $("stage").tabIndex = -1;
    $("stage").focus({ preventScroll: true });
  }
  const active = buttons.indexOf(document.activeElement);
  const focusApplied = active === 0 ? "first" : active === 1 ? "second" : "none";

  let t0 = null;
  let exposureId = null;
  // Two frames: the first schedules the paint, the second runs after it, so
  // t0 is when the options were actually on screen.
  requestAnimationFrame(() =>
    requestAnimationFrame(() => {
      t0 = performance.now();
      trace.setT0(t0);
      exposureId = uuid();
      send({
        type: "exposure",
        exposure_id: exposureId,
        trial_index: trial.index,
        block_index: trial.block,
        shown: { options: trial.options, trial: trial.trial },
        rendered: {
          render_delay_ms: Math.round(t0 - renderStart),
          pointer: pointerType() === "fine" ? lastMouse : null,
          focus_applied: focusApplied,
        },
        informed,
        ctx: context(s.version, measureLayout()),
      });
    }),
  );

  const onClick = (e) => {
    if (t0 === null) return; // can't happen in practice: not yet painted
    buttons.forEach((b) => {
      b.removeEventListener("click", onClick);
      b.disabled = true;
    });
    const chosen = e.currentTarget;
    const { latency, input, trace: tr } = trace.finish(e, chosen);
    send({
      type: "choice",
      exposure_id: exposureId,
      chosen_slot: Number(chosen.dataset.slot),
      latency_ms: latency,
      input,
      trace: tr,
      ctx: context(s.version, measureLayout()),
    });

    progress.next = trial.index + 1;
    storage.setJSON("nudge.progress", progress);

    if (i + 1 < s.block.length) {
      show("gap", "Pick one.", primeText());
      setTimeout(() => runTrial(i + 1), GAP_MS);
    } else if (trial.block === 0) {
      blockEnd(trial.block);
    } else {
      // Block boundary: same gap as usual, held open only if the next block
      // is still on its way.
      show("gap", "Pick one.", primeText());
      const next = upcoming || fetchNextBlock();
      upcoming = null;
      Promise.all([next, new Promise((r) => setTimeout(r, GAP_MS))]).then(([trials]) => {
        s.block = trials;
        runTrial(0);
      });
    }
  };
  buttons.forEach((b) => b.addEventListener("click", onClick));
}

// ---- Between blocks -------------------------------------------------------

let nextBlock = null;
let endedBlock = null;

function blockEnd(blockIndex) {
  endedBlock = blockIndex;
  $("progress").textContent = "";
  nextBlock = loadBlock(); // fetch while they decide
  show("block", "Keep going?");
  $("keep-going").focus({ preventScroll: true });
}

function endBlock(action) {
  send({ type: "block_end", block_index: endedBlock, action, ctx: context(s.version, measureLayout()) });
}

async function continuePlaying(btn) {
  btn.disabled = true;
  await nextBlock;
  btn.disabled = false;
  runTrial(0);
}

$("keep-going").addEventListener("click", (e) => {
  endBlock("keep_going");
  continuePlaying(e.currentTarget);
});

$("done").addEventListener("click", () => {
  endBlock("done");
  // Anything played after reading the explanation is flagged as informed.
  informed = true;
  storage.set("nudge.informed", "1");
  show("done", "Thanks for playing!", "Here's what was going on.");
  $("play-more").focus({ preventScroll: true });
});

$("play-more").addEventListener("click", (e) => continuePlaying(e.currentTarget));

init();
