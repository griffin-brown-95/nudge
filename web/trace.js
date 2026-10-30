// Records how a visitor arrived at a choice during one trial.
//   mouse/pen: enter/leave/down sequence over the options and dwell per option
//   touch:     time to first touch, scrolling before the tap, tap position
//   keyboard:  order in which options received focus
// Times are ms since the options rendered (t0, a performance.now() value,
// set with setT0 once the first frame showing the options has painted).
const MAX_EVENTS = 200; // must match events.MaxTraceLen on the server

export function startTrace(buttons) {
  let t0 = performance.now();
  const pointer = [];
  const focusSeq = [];
  const dwell = [0, 0];
  const enteredAt = [null, null];
  let firstTouch = null;
  let scrolled = 0;
  let lastScrollY = scrollY;
  let lastPointerType = null;
  let programmaticFocus = false;

  const since = (ts) => Math.max(0, Math.round(ts - t0));
  const slotOf = (el) => Number(el.dataset.slot);
  const isHover = (e) => e.pointerType === "mouse" || e.pointerType === "pen";
  const push = (entry) => pointer.length < MAX_EVENTS && pointer.push(entry);

  const on = [];
  const listen = (target, type, fn, opts) => {
    target.addEventListener(type, fn, opts);
    on.push(() => target.removeEventListener(type, fn, opts));
  };

  for (const btn of buttons) {
    const s = slotOf(btn);
    listen(btn, "pointerenter", (e) => {
      if (!isHover(e)) return;
      push({ t: since(e.timeStamp), e: "enter", slot: s });
      enteredAt[s - 1] = e.timeStamp;
    });
    listen(btn, "pointerleave", (e) => {
      if (!isHover(e)) return;
      push({ t: since(e.timeStamp), e: "leave", slot: s });
      if (enteredAt[s - 1] != null) {
        dwell[s - 1] += e.timeStamp - enteredAt[s - 1];
        enteredAt[s - 1] = null;
      }
    });
    listen(btn, "pointerdown", (e) => {
      if (isHover(e)) push({ t: since(e.timeStamp), e: "down", slot: s });
    });
    listen(btn, "focus", () => {
      if (!programmaticFocus && focusSeq.length < MAX_EVENTS) focusSeq.push(s);
    });
  }
  listen(window, "pointerdown", (e) => {
    lastPointerType = e.pointerType || "mouse";
    if (e.pointerType === "touch" && firstTouch == null) firstTouch = since(e.timeStamp);
  }, { capture: true });
  listen(window, "scroll", () => {
    scrolled += Math.abs(scrollY - lastScrollY);
    lastScrollY = scrollY;
  }, { passive: true });

  return {
    setT0(v) {
      t0 = v;
    },

    // Focus set by the experiment itself (default_focus) isn't a visitor action.
    focusProgrammatically(btn) {
      programmaticFocus = true;
      btn.focus({ preventScroll: true });
      programmaticFocus = false;
    },

    // Called with the click that made the choice.
    finish(click, chosen) {
      on.forEach((off) => off());
      const latency = since(click.timeStamp);
      // A click with detail 0 came from Enter/Space, not a pointer.
      const input = click.detail === 0 ? "keyboard" : lastPointerType || "mouse";
      const trace = {};

      if (pointer.length) trace.pointer = pointer;
      if (input === "mouse" || input === "pen") {
        enteredAt.forEach((at, i) => {
          if (at != null) dwell[i] += click.timeStamp - at;
        });
        trace.dwell_ms = dwell.map((d) => Math.min(Math.round(d), latency));
      }
      if (input === "touch") {
        if (firstTouch != null) trace.first_touch_ms = Math.min(firstTouch, latency);
        trace.scroll_px = Math.round(scrolled);
        const r = chosen.getBoundingClientRect();
        const clamp = (v) => Math.min(1, Math.max(0, Math.round(v * 1000) / 1000));
        trace.tap = {
          slot: slotOf(chosen),
          x: clamp((click.clientX - r.left) / r.width),
          y: clamp((click.clientY - r.top) / r.height),
        };
      }
      if (focusSeq.length) trace.focus_seq = focusSeq;
      return { latency, input, trace };
    },
  };
}
