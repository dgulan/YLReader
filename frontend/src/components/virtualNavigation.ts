// Find the next enabled row without wrapping or selecting it.
export function enabledRow(items: readonly { disabled?: boolean }[], from: number, direction: 1 | -1) {
  for (let i = from; i >= 0 && i < items.length; i += direction) {
    if (!items[i]!.disabled) return i
  }
  return -1
}
