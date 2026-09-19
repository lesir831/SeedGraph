import { afterEach, describe, expect, it, vi } from 'vitest'
import { toAuditEvent, toDeleteJob } from '../src/api/transformers'
import { deleteJobProgress, isDeleteJobTerminal, loadTrackedJobIds, saveTrackedJobIds } from '../src/deletion/progress'

afterEach(() => {
  vi.restoreAllMocks()
  sessionStorage.clear()
})

describe('deletion status and progress', () => {
  it('retains verification and uncertain states hidden by the public status', () => {
    const job = toDeleteJob({
      id: 'job', plan_id: 'plan', status: 'running', internal_status: 'verifying', created_at: 'now',
      steps: [{ id: 'step', position: 1, instance_id: 'instance', downloader_id: 'downloader', delete_data: true, status: 'completed' }],
    })
    expect(job.status).toBe('verifying')
    expect(job.steps[0]).toMatchObject({ order: 1, deleteData: true })
    expect(deleteJobProgress(job)).toBe(50)
    expect(isDeleteJobTerminal(job)).toBe(false)
    expect(deleteJobProgress({ ...job, status: 'completed' })).toBe(100)

    const uncertain = toDeleteJob({ id: 'job', plan_id: 'plan', status: 'failed', internal_status: 'uncertain', created_at: 'now' })
    expect(uncertain.status).toBe('uncertain')
    expect(isDeleteJobTerminal(uncertain)).toBe(true)
    expect(deleteJobProgress(uncertain)).toBe(0)
  })

  it('restores unique job IDs and tolerates corrupt or unavailable storage', () => {
    saveTrackedJobIds(['job-a', 'job-a', 'job-b'])
    expect(loadTrackedJobIds()).toEqual(['job-a', 'job-b'])
    sessionStorage.setItem('seedgraph.delete-jobs', 'invalid')
    expect(loadTrackedJobIds()).toEqual([])
    sessionStorage.setItem('seedgraph.delete-jobs', '[null,42,"","job"]')
    expect(loadTrackedJobIds()).toEqual(['job'])
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable') })
    expect(() => saveTrackedJobIds(['job'])).not.toThrow()
  })

  it('preserves complete audit details and warning outcomes for uncertain deletions', () => {
    expect(toAuditEvent({
      id: 'audit', action: 'delete.uncertain', status: 'warning', target_type: 'delete_job', target_id: 'job',
      created_at: 'now', details: { error: 'timeout', instances: ['one', 'two'] },
    })).toMatchObject({ status: 'warning', message: 'timeout', details: { instances: ['one', 'two'] } })
  })
})
