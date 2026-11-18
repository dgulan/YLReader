import { describe, it, expect } from 'vitest'
import { enabledRow } from './virtualNavigation'

describe('virtual directory keyboard navigation', () => {
  const items = [{ disabled: true }, {}, { disabled: true }, {}, { disabled: true }]
  it('skips volumes and disabled rows in both directions', () => {
    expect(enabledRow(items, 0, 1)).toBe(1)
    expect(enabledRow(items, 2, 1)).toBe(3)
    expect(enabledRow(items, 4, -1)).toBe(3)
    expect(enabledRow(items, 2, -1)).toBe(1)
  })
  it('does not wrap, focus an unavailable row, or select an empty list', () => {
    expect(enabledRow(items, -1, -1)).toBe(-1)
    expect(enabledRow(items, 5, 1)).toBe(-1)
    expect(enabledRow([{ disabled: true }], 0, 1)).toBe(-1)
    expect(enabledRow([], 0, 1)).toBe(-1)
  })
})
