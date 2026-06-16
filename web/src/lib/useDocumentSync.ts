import { useEffect, useRef, useState } from 'react'

// Shape returned by useDocumentSync — what every consumer needs to
// render: the data, the error, and a "first-fetch hasn't finished
// yet" flag for sites that want a one-time spinner.
export interface DocumentSyncState<T> {
  data: T | undefined
  error: string | null
  firstFetchPending: boolean
}

// Options that genuinely differ between call sites:
//
// - skipIf: predicate called BEFORE applying a refetch result. Used
//   by NotesPanel to refuse server-echo overwrites while the user
//   is mid-typing. Only consulted on refetches, never on the very
//   first fetch (we always want to hydrate the empty state).
export interface DocumentSyncOptions<T> {
  skipIf?: (next: T) => boolean
}

// useDocumentSync runs `fetcher` whenever any value in `deps`
// changes (typically [key, counter] where counter is bumped by a
// WS event). Standard React useEffect rules apply — `fetcher` and
// `deps` are read together; bump the counter to force a refetch.
//
// The hook handles:
//   - alive flag so a stale fetch can't write into a remounted state
//   - first-fetch-pending tracking (cleared once on first finally)
//   - error normalization (Error.message, else String(e))
//   - optional skipIf gate on refetches (not first fetch)
//   - optional 404 → data=undefined, error=null
//
// What it does NOT handle (intentionally):
//   - any "loading" boolean beyond firstFetchPending. Sites that
//     want a per-refetch spinner (RecapSidebar content) keep their
//     own useEffect — that policy doesn't fit the "spinner once,
//     silent refetches" shape this hook embodies.
//   - writes / PUTs. Bidirectional sync stays in the component
//     because the conflict policy (lastPushedRef, debounce) is
//     site-specific (see NotesPanel).
//   - 404 → empty. Each call site translates 404 according to its
//     own model (RecapSidebar treats 404 as empty content).
export function useDocumentSync<T>(
  fetcher: () => Promise<T>,
  deps: ReadonlyArray<unknown>,
  options: DocumentSyncOptions<T> = {},
): DocumentSyncState<T> {
  const [data, setData] = useState<T | undefined>(undefined)
  const [error, setError] = useState<string | null>(null)
  const [firstFetchPending, setFirstFetchPending] = useState(true)
  const firstFetchDoneRef = useRef(false)
  const skipIfRef = useRef(options.skipIf)
  skipIfRef.current = options.skipIf

  useEffect(() => {
    let alive = true
    const isFirstFetch = !firstFetchDoneRef.current
    fetcher()
      .then((value) => {
        if (!alive) return
        if (!isFirstFetch && skipIfRef.current?.(value)) return
        setData(value)
        setError(null)
      })
      .catch((e) => {
        if (!alive) return
        setError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!alive) return
        if (!firstFetchDoneRef.current) {
          firstFetchDoneRef.current = true
          setFirstFetchPending(false)
        }
      })
    return () => { alive = false }
    // fetcher is intentionally NOT in deps — callers pass `deps`
    // explicitly so they control when refetch happens. The fetcher
    // closure is read on every effect run anyway.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)

  return { data, error, firstFetchPending }
}
