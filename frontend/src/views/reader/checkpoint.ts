// Ordinary callers share one outcome; only explicit deferred intents run later.
export function singleFlight<T>(run: () => Promise<T>) {
  let pending: Promise<T> | undefined
  const invoke = () => {
    pending ??= Promise.resolve().then(run).finally(() => { pending = undefined })
    return pending
  }
  // An explicit reconnect intent must survive an older in-flight failure.
  // Ordinary callers still share that outcome without scheduling retries.
  return Object.assign(invoke, { afterPending: (next: () => Promise<T>) => pending ? pending.then(next, next) : next() })
}

export function watchPageCheckpoint(checkpoint: () => void, doc: Document = document, win: Window = window) {
  const visibility = () => { if (doc.visibilityState === 'hidden') checkpoint() }
  doc.addEventListener('visibilitychange', visibility)
  win.addEventListener('pagehide', checkpoint)
  return () => {
    doc.removeEventListener('visibilitychange', visibility)
    win.removeEventListener('pagehide', checkpoint)
  }
}
