<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      role="dialog"
      aria-modal="true"
      @click.self="close"
    >
      <div class="flex max-h-[88vh] w-full max-w-5xl flex-col rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
        <div class="flex items-center justify-between gap-3 border-b border-gray-200 px-5 py-3 dark:border-dark-700">
          <strong class="text-sm">
            {{ conversation ? t('admin.conversationArchive.detail.title', { id: conversation.id }) : t('admin.conversationArchive.detail.loading') }}
          </strong>
          <button type="button" class="btn btn-secondary btn-xs" @click="close">
            {{ t('admin.conversationArchive.actions.close') }}
          </button>
        </div>

        <div class="flex-1 overflow-auto px-5 py-4">
          <p v-if="error" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>

          <template v-else-if="conversation">
            <dl class="mb-4 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-[13px]">
              <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.conversationArchive.detail.window') }}</dt>
              <dd class="font-mono">{{ formatDate(conversation.started_at) }} → {{ formatDate(conversation.updated_at) }}</dd>
              <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.conversationArchive.table.user') }}</dt>
              <dd class="font-mono">
                {{ conversation.username || '—' }} (#{{ conversation.user_id }})
                <template v-if="conversation.user_email"> · {{ conversation.user_email }}</template>
              </dd>
              <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.conversationArchive.table.apiKey') }}</dt>
              <dd class="font-mono">{{ conversation.api_key_name || '—' }} (#{{ conversation.api_key_id }})</dd>
              <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.conversationArchive.detail.route') }}</dt>
              <dd class="font-mono">{{ conversation.provider }} · {{ conversation.endpoint }} · {{ conversation.model }}</dd>
              <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.conversationArchive.detail.scale') }}</dt>
              <dd class="font-mono">
                {{
                  t('admin.conversationArchive.detail.scaleValue', {
                    r: conversation.requests,
                    t: conversation.turns,
                    k: conversation.tool_calls,
                    c: conversation.chars.toLocaleString(),
                  })
                }}
              </dd>
              <template v-if="conversation.truncated">
                <dt class="text-amber-600 dark:text-amber-400">{{ t('admin.conversationArchive.detail.notice') }}</dt>
                <dd class="text-amber-600 dark:text-amber-400">{{ t('admin.conversationArchive.detail.truncated') }}</dd>
              </template>
              <template v-if="conversation.text_purged_at">
                <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.conversationArchive.detail.purgedAt') }}</dt>
                <dd class="font-mono">{{ formatDate(conversation.text_purged_at) }}</dd>
              </template>
            </dl>

            <pre
              class="whitespace-pre-wrap break-words rounded-lg border border-gray-200 bg-gray-50 p-3 font-mono text-[12.5px] dark:border-dark-700 dark:bg-dark-900"
              >{{ transcript }}</pre
            >

            <details class="mt-4">
              <summary class="cursor-pointer text-[13px] text-gray-500 dark:text-dark-400">
                {{ t('admin.conversationArchive.detail.timeline') }}
              </summary>
              <div class="mt-2 overflow-x-auto">
                <table class="w-full text-xs">
                  <thead class="text-gray-500 dark:text-dark-400">
                    <tr class="border-b border-gray-200 dark:border-dark-700">
                      <th class="px-2 py-1 text-left">{{ t('admin.conversationArchive.table.lastActive') }}</th>
                      <th class="px-2 py-1 text-right">{{ t('admin.conversationArchive.detail.gap') }}</th>
                      <th class="px-2 py-1 text-left">{{ t('admin.conversationArchive.table.route') }}</th>
                      <th class="px-2 py-1 text-right">{{ t('admin.conversationArchive.table.turns') }}</th>
                      <th class="px-2 py-1 text-right">{{ t('admin.conversationArchive.table.toolCalls') }}</th>
                      <th class="px-2 py-1 text-left">Request ID</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="row in timeline" :key="row.id" class="border-b border-gray-100 last:border-0 dark:border-dark-700">
                      <td class="px-2 py-1 font-mono">{{ formatDate(row.captured_at) }}</td>
                      <td class="px-2 py-1 text-right font-mono">{{ row.gap }}</td>
                      <td class="px-2 py-1 font-mono">{{ row.model }}</td>
                      <td class="px-2 py-1 text-right tabular-nums">{{ row.turns }}</td>
                      <td class="px-2 py-1 text-right tabular-nums">{{ row.tool_calls }}</td>
                      <td class="px-2 py-1 font-mono text-gray-400 dark:text-dark-500">{{ row.request_id.slice(0, 8) }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </details>
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getConversation, listRequests } from '../api'
import type { ArchiveConversation, ArchiveRequest } from '../types'

const props = defineProps<{ open: boolean; conversationId: number | null }>()
const emit = defineEmits<{ 'update:open': [boolean] }>()

const { t } = useI18n()

const conversation = ref<ArchiveConversation | null>(null)
const requests = ref<ArchiveRequest[]>([])
const error = ref('')

const transcript = computed(() => {
  if (!conversation.value) return ''
  if (conversation.value.text_purged_at) return t('admin.conversationArchive.detail.purgedBody')
  return conversation.value.text || t('admin.conversationArchive.detail.emptyBody')
})

/** The gap column is what makes an agent loop legible: one task, many calls. */
const timeline = computed(() => {
  let previous: number | null = null
  return requests.value.map((r) => {
    const at = new Date(r.captured_at).getTime()
    const gap = previous === null ? '—' : `${Math.round((at - previous) / 1000)}s`
    previous = at
    return { ...r, gap }
  })
})

function formatDate(value?: string | null): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function close() {
  emit('update:open', false)
}

watch(
  () => [props.open, props.conversationId] as const,
  async ([open, id]) => {
    if (!open || !id) return
    conversation.value = null
    requests.value = []
    error.value = ''
    try {
      conversation.value = await getConversation(id)
    } catch (err) {
      error.value = (err as Error).message
      return
    }
    try {
      requests.value = await listRequests(id)
    } catch {
      requests.value = []
    }
  },
  { immediate: true },
)
</script>
