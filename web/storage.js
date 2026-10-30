// localStorage with an in-memory fallback for private modes and blocked
// storage. No cookies are ever used.
const memory = new Map();

export const storage = {
  get(key) {
    try {
      return localStorage.getItem(key);
    } catch {
      return memory.has(key) ? memory.get(key) : null;
    }
  },
  set(key, value) {
    try {
      localStorage.setItem(key, value);
    } catch {
      memory.set(key, value);
    }
  },
  getJSON(key, fallback) {
    try {
      const v = this.get(key);
      return v == null ? fallback : JSON.parse(v);
    } catch {
      return fallback;
    }
  },
  setJSON(key, value) {
    this.set(key, JSON.stringify(value));
  },
};

export function uuid() {
  if (crypto.randomUUID) return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}
