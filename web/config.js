// Where the nudge API lives. Local dev talks to `make run-local` on :8080.
const LOCAL = ["localhost", "127.0.0.1"].includes(location.hostname);

export const API_BASE = LOCAL
  ? "http://localhost:8080"
  : "https://nudge-370784999851.us-east1.run.app"; // Cloud Run, project nudge-513203

// How long to wait for /trials before falling back to unlogged offline trials.
export const TRIALS_TIMEOUT_MS = 8000;
// Fixed blank between trials, with a centered dot.
export const GAP_MS = 500;
