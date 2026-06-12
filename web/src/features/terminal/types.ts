// Re-export so existing imports `import { CompletedMsg, RunningCmd }
// from './types'` keep working. The source of truth lives in
// features/sessions/types.ts now.
export type { RunningCmd, CompletedMsg } from '../sessions/types'
