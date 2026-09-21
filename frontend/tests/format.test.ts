import { describe, expect, it } from 'vitest'
import { formatBytes, formatDeleteBlocker, formatDuration, formatPercent, formatRatio, formatRatioRange, formatOptionalBytes, formatSpeed } from '../src/utils/format'

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
    expect(formatDeleteBlocker('file_manifest_missing', 'missing')).toContain('同步下载器')
    expect(formatDeleteBlocker('future_code', 'server explanation')).toBe('server explanation')
  })
})


describe('torrent runtime formatting', () => {
  it('distinguishes missing readings from actual zero values', () => {
    expect(formatRatio(0)).toBe('0.00')
    expect(formatRatio(undefined)).toBe('—')
    expect(formatRatio(-1)).toBe('—')
    expect(formatRatio(Number.NaN)).toBe('—')
    expect(formatOptionalBytes(undefined)).toBe('—')
    expect(formatOptionalBytes(0)).toBe('0 B')
    expect(formatSpeed(undefined)).toBe('—')
    expect(formatSpeed(1024)).toBe('1.0 KB/s')
  })

  it('shows a range for different task ratios', () => {
    expect(formatRatioRange(2.5, 4)).toBe('2.50 – 4.00')
    expect(formatRatioRange(2.5, 2.5)).toBe('2.50')
    expect(formatRatioRange(undefined, undefined)).toBe('—')
  })
})
