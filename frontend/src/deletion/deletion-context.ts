import { createContext, useContext } from 'react'
import type { DeleteJob } from '../api/types'

export const DeletionContext = createContext<{ trackJob: (job: DeleteJob) => void } | undefined>(undefined)

export function useDeletionTasks() {
  const context = useContext(DeletionContext)
  if (!context) throw new Error('DeletionTasksProvider is missing')
  return context
}
