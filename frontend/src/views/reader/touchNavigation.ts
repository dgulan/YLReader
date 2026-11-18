export interface TouchPoint {
  x: number
  y: number
  time: number
}

export type SwipeDirection = 'next' | 'prev' | null

export function detectHorizontalSwipe(
  start: TouchPoint | null,
  end: TouchPoint,
  hasSelection = false,
): SwipeDirection {
  if (!start || hasSelection) return null
  const dt = end.time - start.time
  if (dt <= 0 || dt > 600) return null
  const dx = end.x - start.x
  const dy = end.y - start.y
  if (Math.abs(dx) < 72 || Math.abs(dy) > 36 || Math.abs(dx) < Math.abs(dy) * 2) {
    return null
  }
  return dx < 0 ? 'next' : 'prev'
}
