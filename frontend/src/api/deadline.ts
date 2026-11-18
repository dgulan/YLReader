export class RequestDeadlineError extends Error {
  constructor() { super('request deadline exceeded'); this.name = 'RequestDeadlineError' }
}
export async function withDeadline<T>(run: (signal: AbortSignal) => Promise<T>, milliseconds = 15_000): Promise<T> {
  const controller = new AbortController()
  let timer: ReturnType<typeof setTimeout> | undefined
  const deadline = new Promise<never>((_, reject) => {
    timer = setTimeout(() => { const error = new RequestDeadlineError(); controller.abort(error); reject(error) }, milliseconds)
  })
  try { return await Promise.race([Promise.resolve().then(() => run(controller.signal)), deadline]) }
  finally { clearTimeout(timer) }
}
