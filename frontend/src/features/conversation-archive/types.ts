/**
 * Conversation archive types.
 *
 * These mirror what sub2api-sidecar returns; the gateway proxies its responses
 * unchanged, so this file is the contract with that service.
 */

export interface ArchiveConversation {
  id: number
  user_id: number
  username: string
  user_email: string
  api_key_id: number
  api_key_name: string
  group_id: number | null
  group_name: string
  provider: string
  endpoint: string
  protocol: string
  model: string
  stage: string
  started_at: string
  updated_at: string
  /** Gateway calls this conversation took: an agent loop is one conversation and many requests. */
  requests: number
  turns: number
  tool_calls: number
  chars: number
  hash: string
  truncated: boolean
  /** Only present on the detail response; listings omit the transcript. */
  text?: string
  text_purged_at?: string | null
  first_request_id: string
  last_request_id: string
}

export interface ArchiveRequest {
  id: number
  conversation_id: number
  request_id: string
  captured_at: string
  model: string
  stage: string
  turns: number
  tool_calls: number
  chars: number
}

export interface ArchiveUserSummary {
  user_id: number
  username: string
  user_email: string
  conversations: number
  requests: number
  first_seen: string
  last_seen: string
  api_keys: number
  total_chars: number
}

export interface ArchiveStats {
  conversations: number
  requests: number
  purged: number
  oldest?: string
  newest?: string
  bytes_text: number
}

/** What /admin/archive/status reports, so the page can explain itself. */
export interface ArchiveStatus {
  enabled: boolean
  reachable?: boolean
  reason?: string
  error?: string
  archive?: {
    consumer?: { stored: number; duplicate: number; poison: number; queue_depth: number }
    store?: ArchiveStats
  }
}

export interface ArchiveFilters {
  q: string
  user: string
  user_id: string
  api_key_id: string
  protocol: string
  model: string
  from: string
  to: string
}

export interface ArchivePage {
  total: number
  limit: number
  offset: number
  items: ArchiveConversation[]
}

export function emptyFilters(): ArchiveFilters {
  return { q: '', user: '', user_id: '', api_key_id: '', protocol: '', model: '', from: '', to: '' }
}
