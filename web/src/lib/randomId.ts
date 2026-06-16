// randomId returns a unique-enough string id for use as a React key,
// turn id, etc. — NOT a cryptographic identifier.
//
// Why we need this: crypto.randomUUID() is only available in secure
// contexts (HTTPS or http://localhost). Alfred is reached over an
// ssh -L tunnel as `http://alfred.local:<port>/`, which is NOT a
// secure context — `alfred.local` is just a custom hostname pointed
// at 127.0.0.1 via /etc/hosts, and the browser doesn't grant secure-
// context privileges to anything other than the literal 'localhost'.
//
// The fallback (Math.random + Date.now) is fine because nothing in
// the codebase relies on these ids being unguessable or globally
// unique across the universe. They only need to be unique within
// one running React app's lifetime.
export function randomId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  // Time prefix + 8 random bytes → ~ULID-ish, sortable, ~zero
  // collision risk in a single-tab session lifetime.
  const t = Date.now().toString(36)
  const r = Math.random().toString(36).slice(2, 10) + Math.random().toString(36).slice(2, 10)
  return `${t}-${r}`
}
