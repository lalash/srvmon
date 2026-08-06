const KB = 1024;
const MB = KB * 1024;
const GB = MB * 1024;
const TB = GB * 1024;
const PB = TB * 1024;

export const WARN_PERCENT = 80;
export const CRIT_PERCENT = 90;

// Same unit ladder and thresholds the panel's overview uses, so a value reads
// identically in both places.
export function sizeFormat(size) {
  if (size == null || !Number.isFinite(size) || size <= 0) return '0 B';
  if (size < KB) return `${size.toFixed(0)} B`;
  if (size < MB) return `${(size / KB).toFixed(2)} KB`;
  if (size < GB) return `${(size / MB).toFixed(2)} MB`;
  if (size < TB) return `${(size / GB).toFixed(2)} GB`;
  if (size < PB) return `${(size / TB).toFixed(2)} TB`;
  return `${(size / PB).toFixed(2)} PB`;
}

export function speedFormat(bps) {
  return `${sizeFormat(bps)}/s`;
}

// Axis ticks drop the decimals: "64 MB/s" fits the gutter, "64.35 MB/s" does not.
export function speedFormatShort(bps) {
  return `${sizeFormat(bps).replace(/\.\d+/, '')}/s`;
}

export function cpuSpeedFormat(mhz) {
  if (!mhz) return '—';
  return mhz > 1000 ? `${(mhz / 1000).toFixed(2)} GHz` : `${mhz.toFixed(2)} MHz`;
}

export function coreFormat(cores) {
  return cores === 1 ? '1 Core' : `${cores || 0} Cores`;
}

export function formatSecond(seconds) {
  const value = Number(seconds) || 0;
  if (value < 60) return `${value.toFixed(0)}s`;
  if (value < 3600) return `${(value / 60).toFixed(0)}m`;
  if (value < 86400) return `${(value / 3600).toFixed(0)}h`;
  const days = Math.floor(value / 86400);
  const hours = Math.round(value / 3600 - days * 24);
  return `${days}d${hours > 0 ? ` ${hours}h` : ''}`;
}

export function formatClock(unixSeconds) {
  const date = new Date(unixSeconds * 1000);
  const pad = (n) => String(n).padStart(2, '0');
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

export function formatDateTime(unixSeconds) {
  if (!unixSeconds) return '—';
  const date = new Date(unixSeconds * 1000);
  const pad = (n) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ` +
    `${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

export function percentOf(part) {
  if (!part || !part.total) return 0;
  return (part.current / part.total) * 100;
}

export function usageColor(percent) {
  if (percent >= CRIT_PERCENT) return 'var(--crit)';
  if (percent >= WARN_PERCENT) return 'var(--warn)';
  return 'var(--primary)';
}

export function mean(values) {
  if (!values || !values.length) return 0;
  return values.reduce((total, value) => total + value, 0) / values.length;
}

export function peak(values) {
  if (!values || !values.length) return 0;
  return values.reduce((max, value) => (value > max ? value : max), 0);
}

export function relativeTime(unixSeconds, t) {
  if (!unixSeconds) return t('never');
  const delta = Math.max(0, Math.floor(Date.now() / 1000) - unixSeconds);
  if (delta < 5) return t('justNow');
  return t('agoValue', { value: formatSecond(delta) });
}

export function escapeHtml(value) {
  return String(value ?? '').replace(/[&<>"']/g, (char) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[char]);
}
