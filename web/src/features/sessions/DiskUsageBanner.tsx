import { DiskUsage } from '../../lib/ws'
import './DiskUsageBanner.css'

interface Props {
  usage: DiskUsage | null
}

// DiskUsageBanner shows a strip across the top of the workspace
// when the PVC is filling up. Rendered nothing below 80%; warning
// (yellow) at 80%, critical (red) at 95%. Backend pushes a
// disk_usage WS frame on threshold crossings (and on first connect)
// — we don't poll from the frontend.
//
// We deliberately don't show the banner in the 0–80% range. Showing
// "you have plenty of space" all the time would be noise; the point
// is to surface the problem when it matters.
export function DiskUsageBanner({ usage }: Props) {
  if (!usage) return null
  const level = alertLevel(usage.usedPercent)
  if (level === 'ok') return null
  return (
    <div className={`disk-banner disk-banner--${level}`} role="status">
      <span className="disk-banner__icon" aria-hidden>
        {level === 'critical' ? '⚠' : '⚠'}
      </span>
      <span className="disk-banner__text">
        <strong>Disk {usage.usedPercent}% full</strong> —{' '}
        {formatBytes(usage.usedBytes)} of {formatBytes(usage.usedBytes + usage.availableBytes)} used
        {level === 'critical'
          ? '. Writes are about to fail. Delete old sessions or grow the PVC.'
          : '. Consider cleaning up before it fills.'}
      </span>
    </div>
  )
}

function alertLevel(percent: number): 'ok' | 'warning' | 'critical' {
  if (percent >= 95) return 'critical'
  if (percent >= 80) return 'warning'
  return 'ok'
}

// 5 GiB → "5.0 GiB", 1024 → "1.0 KiB", etc. Binary prefixes match
// what `df -h` shows on Linux. Small numbers stay byte-precise.
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}
