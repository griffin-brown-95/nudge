// Durable, ordered event queue. Events are saved to storage first and sent
// one at a time, so a cold-starting or unreachable API never blocks the
// visitor and nothing is lost on reload.
//
// Server responses (see nudge/internal/httpapi):
//   202        stored            -> drop from queue
//   400/413/415 invalid or stale  -> drop (retrying can't help)
//   409        parent not stored -> retry later (a few times, then drop)
//   429/5xx/network               -> retry later with backoff
import { API_BASE } from "./config.js";
import { storage } from "./storage.js";

const KEY = "nudge.queue";
const MAX_QUEUE = 1000;
const MAX_CONFLICT_RETRIES = 20;
const MAX_BACKOFF_MS = 30_000;

let queue = storage.getJSON(KEY, []);
let flushing = false;
let backoff = 0;
let timer = null;

function save() {
  storage.setJSON(KEY, queue);
}

export function enqueue(event) {
  queue.push({ event, conflicts: 0 });
  if (queue.length > MAX_QUEUE) queue = queue.slice(-MAX_QUEUE);
  save();
  flush();
}

export function pending() {
  return queue.length;
}

export async function flush() {
  if (flushing) return;
  flushing = true;
  clearTimeout(timer);
  try {
    while (queue.length) {
      const item = queue[0];
      let res;
      try {
        res = await fetch(`${API_BASE}/event`, {
          method: "POST",
          // text/plain keeps this a CORS "simple request": no preflight.
          headers: { "Content-Type": "text/plain;charset=UTF-8" },
          body: JSON.stringify(item.event),
          keepalive: true,
        });
      } catch {
        return retryLater();
      }

      if (res.status === 202 || [400, 413, 415].includes(res.status)) {
        if (res.status !== 202) console.warn("nudge: event dropped", res.status, await res.text());
        queue.shift();
        save();
        backoff = 0;
        continue;
      }
      if (res.status === 409 && ++item.conflicts > MAX_CONFLICT_RETRIES) {
        queue.shift(); // its parent was lost for good
        save();
        continue;
      }
      save();
      return retryLater();
    }
  } finally {
    flushing = false;
  }
}

function retryLater() {
  backoff = Math.min(MAX_BACKOFF_MS, backoff ? backoff * 2 : 1000);
  timer = setTimeout(flush, backoff);
}

// Try once more when the tab is hidden or closed; keepalive lets the
// request outlive the page.
addEventListener("visibilitychange", () => {
  if (document.visibilityState === "hidden") flush();
});
addEventListener("online", flush);
