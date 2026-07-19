import { describe, expect, it } from 'vitest'
import {
  filterInstanceOptions,
  normalizePagedResponse,
  summarizeGroup,
  toAuthSession,
  toDeletePlan,
  toDownloader,
  toOverview,
  toTorrentGroup,
  toUnmappedTrackerIdentity,
} from '../src/api/transformers'
import type { TorrentGroup, TorrentInstance } from '../src/api/types'

const makeInstance = (overrides: Partial<TorrentInstance>): TorrentInstance => ({
  id: 'instance-1',
  downloaderId: 'client-1',
  downloaderName: '家中 qB',
  downloaderKind: 'qbittorrent',
  hash: 'ABC123',
  name: 'Ubuntu ISO',
  savePath: '/downloads/linux',
  totalSize: 100,
  progress: 1,
  ratio: 2,
  state: 'seeding',
  sites: [],
  ...overrides,
})

describe('summarizeGroup', () => {
  it('derives duplicate, downloader and reclaimable metrics', () => {
    const group: TorrentGroup = {
      id: 'group-1',
      name: 'Ubuntu ISO',
      canonicalPath: '/downloads/linux/ubuntu.iso',
      totalSize: 100,
      fileCount: 1,
      files: [{ path: 'ubuntu.iso', size: 100 }],
      instances: [
        makeInstance({ id: 'a', downloaderName: '节点 B', progress: 1 }),
        makeInstance({ id: 'b', downloaderName: '节点 A', progress: 0.5 }),
        makeInstance({ id: 'c', downloaderName: '节点 A', progress: 0 }),
      ],
      groupingMethod: 'automatic',
      version: 3,
      taskCount: 3,
      siteCount: 2,
      downloaderCount: 2,
      dataCopyCount: 3,
      confidence: 'verified',
      stale: false,
      updatedAt: '2026-07-18T00:00:00Z',
    }

    expect(summarizeGroup(group)).toEqual({
      instanceCount: 3,
      duplicateCount: 2,
      downloaderNames: ['节点 A', '节点 B'],
      reclaimableBytes: 200,
      averageProgress: 0.5,
    })
  })
})

describe('API model helpers', () => {
  it('normalizes bare arrays into a paginated shape', () => {
    expect(normalizePagedResponse(['a', 'b'], 2, 50)).toEqual({
      items: ['a', 'b'],
      total: 2,
      page: 2,
      pageSize: 50,
    })
  })

  it('matches ungrouped instances by several useful fields', () => {
    const items = [
      makeInstance({ id: 'a' }),
      makeInstance({ id: 'b', name: 'Movie', hash: 'XYZ', downloaderName: '远端 Transmission' }),
    ]
    expect(filterInstanceOptions(items, 'transmission').map((item) => item.id)).toEqual(['b'])
    expect(filterInstanceOptions(items, 'abc123').map((item) => item.id)).toEqual(['a'])
  })

  it('maps snake_case group details into view models', () => {
    const group = toTorrentGroup({
      id: 'group-1',
      name: 'Linux ISO',
      size_bytes: 2048,
      task_count: 1,
      site_count: 1,
      downloader_count: 1,
      data_copy_count: 1,
      confidence: 'verified',
      mode: 'auto',
      locked: false,
      version: 2,
      stale: false,
      oldest_added_at: '2026-07-17T12:00:00Z',
      updated_at: '2026-07-18T00:00:00Z',
      instances: [{
        id: 'instance-1',
        downloader_id: 'downloader-1',
        downloader_name: 'NAS qB',
        downloader_kind: 'qbittorrent',
        stable_hash_key: 'abc',
        name: 'Linux ISO',
        canonical_path: '/data/linux.iso',
        storage_id: 'storage-1',
        wanted_bytes: 2048,
        data_group_id: 'data-1',
        assignment_source: 'auto',
        status: 'seeding',
        progress: 1,
        ratio: 2.5,
        added_at: '2026-07-17T12:00:00Z',
        updated_at: '2026-07-18T00:00:00Z',
        sites: ['Example'],
      }],
    })

    expect(group.canonicalPath).toBe('/data/linux.iso')
    expect(group.groupingMethod).toBe('automatic')
    expect(group.oldestAddedAt).toBe('2026-07-17T12:00:00Z')
    expect(group.instances[0]).toMatchObject({
      hash: 'abc',
      trackerHost: 'Example',
      sites: ['Example'],
      addedAt: '2026-07-17T12:00:00Z',
      state: 'seeding',
    })
  })

  it('maps privacy-preserving unmapped Tracker identities', () => {
    expect(toUnmappedTrackerIdentity({
      host_identity: 'tracker.example.com',
      path_hint: '/announce/*',
      instance_count: 3,
      group_count: 2,
      last_seen_at: '2026-07-19T00:00:00Z',
    })).toEqual({
      hostIdentity: 'tracker.example.com',
      pathHint: '/announce/*',
      instanceCount: 3,
      groupCount: 2,
      lastSeenAt: '2026-07-19T00:00:00Z',
    })
  })

  it('derives overview and downloader health without leaking wire naming', () => {
    expect(toOverview({
      logical_resources: 8,
      torrent_tasks: 11,
      logical_bytes: 800,
      raw_task_bytes: 1100,
      known_sites: 2,
      unknown_trackers: 1,
      online_downloaders: 1,
      total_downloaders: 2,
      stale_groups: 1,
    })).toMatchObject({ duplicateGroupCount: 3, reclaimableBytes: 300, syncStatus: 'warning' })

    expect(toDownloader({
      id: 'd-1',
      name: 'NAS',
      kind: 'transmission',
      base_url: 'http://transmission:9091',
      storage_id: 's-1',
      storage_name: 'NAS volume',
      enabled: true,
      online: false,
      last_error: 'timeout',
    }).health).toBe('degraded')
  })

  it('keeps CSRF sessions and server-owned delete decisions intact', () => {
    expect(toAuthSession({
      authenticated: true,
      subject: 'admin',
      csrf_token: 'csrf-value',
      expires_at: '2026-07-19T00:00:00Z',
    })).toMatchObject({ username: 'admin', csrfToken: 'csrf-value' })

    expect(toDeletePlan({
      id: 'plan-1',
      selected_instance_ids: ['instance-1'],
      executable: false,
      steps: [{
        order: 1,
        instance_id: 'instance-1',
        downloader_id: 'downloader-1',
        content_group_id: 'content-1',
        data_group_id: 'data-1',
        delete_data: true,
      }],
      blockers: [{
        code: 'conflicting_path_occupant',
        message: 'conflict',
        instance_id: 'instance-2',
        instance_name: 'Other torrent',
        downloader_id: 'downloader-2',
        downloader_name: 'NAS',
        path: '/downloads/other',
      }],
    }, 'content-1')).toMatchObject({
      executable: false,
      steps: [{ instanceId: 'instance-1', deleteData: true }],
      blockers: [{
        instanceId: 'instance-2',
        instanceName: 'Other torrent',
        downloaderId: 'downloader-2',
        downloaderName: 'NAS',
        path: '/downloads/other',
      }],
    })
  })
})
