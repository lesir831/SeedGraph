import { describe, expect, it } from 'vitest'
import { formatBytes, formatDeleteBlocker, formatDuration, formatPercent } from '../src/utils/format'

describe('formatBytes', () => {
  it('uses binary units and readable precision', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(5 * 1024 ** 3)).toBe('5.0 GB')
  })

  it('handles invalid and negative input defensively', () => {
    expect(formatBytes(Number.NaN)).toBe('0 B')
    expect(formatBytes(-100)).toBe('0 B')
  })
})

describe('time and percentage formatters', () => {
  it('formats durations at useful boundaries', () => {
    expect(formatDuration(920)).toBe('920 ms')
    expect(formatDuration(65_000)).toBe('1 分 5 秒')
  })

  it('accepts both ratios and percentage values', () => {
    expect(formatPercent(0.755)).toBe('75.5%')
    expect(formatPercent(120)).toBe('100.0%')
  })
})

describe('delete blockers', () => {
  it('localizes stable blocker codes and preserves unknown messages', () => {
    expect(formatDeleteBlocker('downloader_offline', 'offline')).toContain('下载器')
    expect(formatDeleteBlocker('future_code', 'server explanation')).toBe('server explanation')
  })
})
