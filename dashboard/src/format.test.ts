import { describe, expect, it } from 'vitest'

import {
  elapsed,
  formatClock,
  formatDuration,
  formatLatency,
  formatPerHour,
  formatPercent,
  relativeTime,
  shortSHA,
  stripAnsi,
} from './format'

describe('shortSHA', () => {
  it('trims to a readable length and leaves a short input alone', () => {
    expect(shortSHA('a'.repeat(40))).toBe('a'.repeat(12))
    expect(shortSHA('abc')).toBe('abc')
    expect(shortSHA('')).toBe('')
  })
})

describe('relativeTime', () => {
  const now = new Date('2026-01-01T12:00:00Z')

  const cases: { name: string; iso: string; want: string }[] = [
    { name: 'seconds old', iso: '2026-01-01T11:59:58Z', want: 'just now' },
    { name: 'a minute old', iso: '2026-01-01T11:59:00Z', want: '1m ago' },
    { name: 'most of an hour old', iso: '2026-01-01T11:15:00Z', want: '45m ago' },
    { name: 'hours old', iso: '2026-01-01T09:00:00Z', want: '3h ago' },
    { name: 'days old', iso: '2025-12-29T12:00:00Z', want: '3d ago' },
    { name: 'months old', iso: '2025-10-01T12:00:00Z', want: '3mo ago' },
    // A browser clock a little ahead of the server must not read "-4s ago".
    { name: 'apparently in the future', iso: '2026-01-01T12:00:04Z', want: 'just now' },
    { name: 'unparseable', iso: 'not a timestamp', want: '' },
  ]

  for (const tc of cases) {
    it(tc.name, () => {
      expect(relativeTime(tc.iso, now)).toBe(tc.want)
    })
  }
})

describe('formatDuration', () => {
  const cases: { ms: number; want: string }[] = [
    { ms: 0, want: '0ms' },
    { ms: 250, want: '250ms' },
    { ms: 1000, want: '1s' },
    { ms: 42_400, want: '42s' },
    { ms: 60_000, want: '1m 0s' },
    { ms: 132_000, want: '2m 12s' },
    { ms: -1, want: '' },
    { ms: Number.NaN, want: '' },
  ]

  for (const tc of cases) {
    it(`renders ${tc.ms}ms as ${JSON.stringify(tc.want)}`, () => {
      expect(formatDuration(tc.ms)).toBe(tc.want)
    })
  }
})

describe('elapsed', () => {
  const now = new Date('2026-01-01T12:01:00Z')

  it('measures a finished deployment between its own timestamps', () => {
    expect(
      elapsed(
        {
          queued_at: '2026-01-01T11:59:00Z',
          started_at: '2026-01-01T12:00:00Z',
          completed_at: '2026-01-01T12:00:30Z',
        },
        now,
      ),
    ).toBe('30s')
  })

  it('measures a running deployment against the clock', () => {
    expect(elapsed({ queued_at: '2026-01-01T11:59:00Z', started_at: '2026-01-01T12:00:00Z' }, now))
      .toBe('1m 0s')
  })

  it('falls back to the queue time before a build has started', () => {
    expect(elapsed({ queued_at: '2026-01-01T12:00:50Z' }, now)).toBe('10s')
  })

  it('renders nothing it cannot measure', () => {
    expect(elapsed({ queued_at: 'nonsense' }, now)).toBe('')
  })
})

describe('formatClock', () => {
  it('renders a wall-clock time', () => {
    // The offset is the machine's, so only the shape is asserted; asserting the
    // digits would make this test fail in another timezone.
    expect(formatClock('2026-01-01T12:34:56Z')).toMatch(/^\d{2}:\d{2}:56$/)
  })

  it('renders nothing for a value it cannot parse', () => {
    expect(formatClock('')).toBe('')
    expect(formatClock('not a timestamp')).toBe('')
  })
})

describe('stripAnsi', () => {
  const cases: { name: string; line: string; want: string }[] = [
    {
      name: 'a colour code npm actually emits',
      line: '\x1b[91mnpm warn deprecated svgo@1.3.2\x1b[0m',
      want: 'npm warn deprecated svgo@1.3.2',
    },
    { name: 'a reset on its own', line: '\x1b[0m', want: '' },
    // The pattern is anchored on the escape byte, so bracketed text survives.
    {
      name: 'bracketed text that is not an escape',
      line: 'Step 1/9 : FROM [node]',
      want: 'Step 1/9 : FROM [node]',
    },
    {
      name: 'a line with nothing to strip',
      line: 'Successfully built 1e86df1b743a',
      want: 'Successfully built 1e86df1b743a',
    },
    { name: 'an empty line', line: '', want: '' },
  ]

  for (const tc of cases) {
    it(tc.name, () => {
      expect(stripAnsi(tc.line)).toBe(tc.want)
    })
  }
})

describe('formatLatency', () => {
  const cases: { name: string; ms: number; want: string }[] = [
    // Nothing measured, rather than an impossibly quick response.
    { name: 'no measurement', ms: 0, want: '\u2014' },
    // The collector records a fast handler as a fraction of a millisecond, so
    // this has to read as fast rather than as free.
    { name: 'sub-millisecond', ms: 0.4, want: '<1ms' },
    { name: 'milliseconds', ms: 42.6, want: '43ms' },
    { name: 'just under a second', ms: 999, want: '999ms' },
    // A p95 of 1.4 seconds must not be reported as "1s", which is what
    // formatDuration would say.
    { name: 'seconds keep a decimal', ms: 1400, want: '1.4s' },
    { name: 'nonsense', ms: Number.NaN, want: '' },
  ]

  for (const tc of cases) {
    it(tc.name, () => {
      expect(formatLatency(tc.ms)).toBe(tc.want)
    })
  }
})

describe('formatPercent', () => {
  it('keeps the decimal that availability lives in', () => {
    // 99.9% and 100% are the difference between a healthy service and one
    // dropping a request in a thousand; rounding would hide it.
    expect(formatPercent(0.999)).toBe('99.9%')
    expect(formatPercent(1)).toBe('100.0%')
    expect(formatPercent(0)).toBe('0.0%')
    expect(formatPercent(Number.NaN)).toBe('')
  })
})

describe('formatPerHour', () => {
  it('rounds a rate to whole requests', () => {
    expect(formatPerHour(1139.6)).toBe('1,140/h')
    expect(formatPerHour(0)).toBe('0/h')
    expect(formatPerHour(-1)).toBe('')
  })
})
