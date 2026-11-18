import { describe, expect, it } from 'vitest'
import { detectHorizontalSwipe } from './touchNavigation'

describe('detectHorizontalSwipe', () => {
  it('detects clean left/right horizontal chapter swipes', () => {
    expect(detectHorizontalSwipe({ x: 240, y: 300, time: 100 }, { x: 120, y: 308, time: 300 })).toBe('next')
    expect(detectHorizontalSwipe({ x: 100, y: 300, time: 100 }, { x: 220, y: 295, time: 280 })).toBe('prev')
  })

  it('ignores vertical scrolling, slow drags, short taps, and active text selections', () => {
    expect(detectHorizontalSwipe({ x: 200, y: 300, time: 100 }, { x: 150, y: 200, time: 260 })).toBeNull()
    expect(detectHorizontalSwipe({ x: 200, y: 300, time: 100 }, { x: 170, y: 300, time: 200 })).toBeNull()
    expect(detectHorizontalSwipe({ x: 200, y: 300, time: 100 }, { x: 80, y: 300, time: 900 })).toBeNull()
    expect(detectHorizontalSwipe({ x: 240, y: 300, time: 100 }, { x: 120, y: 305, time: 300 }, true)).toBeNull()
  })
})
