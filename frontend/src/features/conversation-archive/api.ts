import { apiClient } from '@/api/client'
import type {
  ArchiveConversation,
  ArchiveFilters,
  ArchivePage,
  ArchiveRequest,
  ArchiveStatus,
  ArchiveUserSummary,
} from './types'

const basePath = '/admin/archive'

/** Drops blank filters so the query string carries only real constraints. */
export function filterParams(filters: ArchiveFilters): Record<string, string> {
  const params: Record<string, string> = {}
  for (const [key, value] of Object.entries(filters)) {
    const trimmed = String(value ?? '').trim()
    if (trimmed) params[key] = trimmed
  }
  return params
}

export async function getStatus(): Promise<ArchiveStatus> {
  const { data } = await apiClient.get<ArchiveStatus>(`${basePath}/status`)
  return data
}

export async function listConversations(
  filters: ArchiveFilters,
  limit: number,
  offset: number,
): Promise<ArchivePage> {
  const { data } = await apiClient.get<ArchivePage>(`${basePath}/conversations`, {
    params: { ...filterParams(filters), limit, offset },
  })
  return data
}

export async function getConversation(id: number): Promise<ArchiveConversation> {
  const { data } = await apiClient.get<ArchiveConversation>(`${basePath}/conversations/${id}`)
  return data
}

export async function listRequests(id: number): Promise<ArchiveRequest[]> {
  const { data } = await apiClient.get<{ items: ArchiveRequest[] }>(
    `${basePath}/conversations/${id}/requests`,
  )
  return data.items ?? []
}

/**
 * The user roll-up answers "who is in what I am currently looking at", so it
 * takes the same filters as the listing — minus user_id, which is the thing it
 * is being used to choose.
 */
export async function listUsers(filters: ArchiveFilters): Promise<ArchiveUserSummary[]> {
  const params = filterParams(filters)
  delete params.user_id
  const { data } = await apiClient.get<{ items: ArchiveUserSummary[] }>(`${basePath}/users`, {
    params,
  })
  return data.items ?? []
}
