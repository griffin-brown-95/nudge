// Context attached to every event. Everything here is coarse on purpose:
// no user agent, no screen fingerprinting, no full referrer URL.
const mq = (q) => matchMedia(q).matches;

export function pointerType() {
  if (mq("(pointer: coarse)")) return "coarse";
  if (mq("(pointer: fine)")) return "fine";
  return "none";
}

function deviceClass() {
  if (pointerType() !== "coarse") return "desktop";
  return Math.min(screen.width, screen.height) < 600 ? "phone" : "tablet";
}

function referrerDomain() {
  try {
    const host = new URL(document.referrer).hostname.toLowerCase();
    return host === location.hostname ? "" : host;
  } catch {
    return "";
  }
}

// "side_by_side" if the two options sit on the same row, else "stacked".
export function measureLayout() {
  const a = document.getElementById("opt1").getBoundingClientRect();
  const b = document.getElementById("opt2").getBoundingClientRect();
  if (!a.height || !b.height) {
    return innerWidth >= 560 ? "side_by_side" : "stacked";
  }
  return Math.abs(a.top - b.top) < a.height / 2 ? "side_by_side" : "stacked";
}

export function context(version, layout = measureLayout()) {
  return {
    device_class: deviceClass(),
    pointer: pointerType(),
    viewport_w: Math.max(1, Math.round(innerWidth)),
    viewport_h: Math.max(1, Math.round(innerHeight)),
    orientation: mq("(orientation: portrait)") ? "portrait" : "landscape",
    layout,
    theme: mq("(prefers-color-scheme: dark)") ? "dark" : "light",
    referrer_domain: referrerDomain(),
    local_hour: new Date().getHours(),
    experiment_version: version,
  };
}
