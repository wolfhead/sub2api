<template>
  <AppLayout>
    <div class="mx-auto max-w-[1600px] pb-8">
      <header class="mb-6">
        <p class="text-xs font-semibold uppercase tracking-[0.16em] text-primary-600 dark:text-primary-400">
          {{ t('nav.securityAudit') }}
        </p>
        <h1 class="mt-1 text-2xl font-semibold tracking-tight text-gray-950 dark:text-white">
          {{ t('admin.conversationArchive.title') }}
        </h1>
        <p class="mt-2 max-w-3xl text-sm text-gray-500 dark:text-dark-300">
          {{ t('admin.conversationArchive.description') }}
        </p>
      </header>

      <!-- Not configured / unreachable are different problems; say which. -->
      <div
        v-if="status && !status.enabled"
        role="alert"
        class="rounded-xl border border-amber-200 bg-amber-50 p-5 dark:border-amber-900 dark:bg-amber-950/30"
      >
        <p class="text-sm text-amber-800 dark:text-amber-200">
          {{ t('admin.conversationArchive.notConfigured') }}
        </p>
        <p v-if="status.reason" class="mt-2 font-mono text-xs text-amber-700 dark:text-amber-300">
          {{ status.reason }}
        </p>
      </div>

      <div
        v-else-if="status && status.reachable === false"
        role="alert"
        class="rounded-xl border border-red-200 bg-red-50 p-5 dark:border-red-900 dark:bg-red-950/30"
      >
        <p class="text-sm text-red-700 dark:text-red-300">
          {{ t('admin.conversationArchive.unreachable') }}
        </p>
        <p v-if="status.error" class="mt-2 font-mono text-xs text-red-600 dark:text-red-400">
          {{ status.error }}
        </p>
        <button type="button" class="btn btn-secondary btn-sm mt-3" @click="loadStatus">
          {{ t('admin.conversationArchive.actions.retry') }}
        </button>
      </div>

      <template v-else>
        <div
          class="mb-4 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200"
        >
          {{ t('admin.conversationArchive.privacyNotice') }}
        </div>

        <div v-if="stats" class="mb-4 flex flex-wrap gap-x-6 gap-y-1 text-xs text-gray-500 dark:text-dark-400">
          <span>{{ t('admin.conversationArchive.stats.conversations', { n: stats.conversations.toLocaleString() }) }}</span>
          <span>{{ t('admin.conversationArchive.stats.requests', { n: stats.requests.toLocaleString() }) }}</span>
          <span>{{ t('admin.conversationArchive.stats.size', { size: formatBytes(stats.bytes_text) }) }}</span>
          <span v-if="stats.purged">{{ t('admin.conversationArchive.stats.purged', { n: stats.purged.toLocaleString() }) }}</span>
          <span v-if="stats.oldest">{{ t('admin.conversationArchive.stats.oldest', { at: formatDate(stats.oldest) }) }}</span>
        </div>

        <form class="mb-4 grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-4" @submit.prevent="search">
          <input
            v-model="filters.q"
            type="search"
            class="input lg:col-span-4"
            :placeholder="t('admin.conversationArchive.filters.query')"
          />
          <input v-model="filters.user" type="text" class="input" :placeholder="t('admin.conversationArchive.filters.user')" list="archive-user-options" />
          <datalist id="archive-user-options">
            <option v-for="name in userOptions" :key="name" :value="name" />
          </datalist>
          <input v-model="filters.model" type="text" class="input" :placeholder="t('admin.conversationArchive.filters.model')" />
          <input v-model="filters.protocol" type="text" class="input" :placeholder="t('admin.conversationArchive.filters.protocol')" />
          <input v-model="filters.api_key_id" type="number" min="1" class="input" :placeholder="t('admin.conversationArchive.filters.apiKeyId')" />
          <input v-model="filters.from" type="datetime-local" class="input" :title="t('admin.conversationArchive.filters.from')" />
          <input v-model="filters.to" type="datetime-local" class="input" :title="t('admin.conversationArchive.filters.to')" />
          <div class="flex gap-2 lg:col-span-2">
            <button type="submit" class="btn btn-primary btn-sm">{{ t('admin.conversationArchive.actions.search') }}</button>
            <button type="button" class="btn btn-secondary btn-sm" @click="reset">{{ t('admin.conversationArchive.actions.reset') }}</button>
          </div>
        </form>

        <!-- Reviewing an archive starts from a person, not from a numeric id. -->
        <section class="mb-4 rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
          <div class="flex items-center gap-3 border-b border-gray-200 px-4 py-2.5 dark:border-dark-700">
            <strong class="text-sm">{{ t('admin.conversationArchive.users.title') }}</strong>
            <span class="flex-1 text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.conversationArchive.users.hint') }}
            </span>
            <button type="button" class="btn btn-secondary btn-xs" @click="usersCollapsed = !usersCollapsed">
              {{ usersCollapsed ? t('admin.conversationArchive.actions.expand') : t('admin.conversationArchive.actions.collapse') }}
            </button>
          </div>
          <div v-if="!usersCollapsed" class="flex flex-wrap gap-2 p-3">
            <span v-if="!users.length" class="text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.conversationArchive.users.empty') }}
            </span>
            <button
              v-for="u in users"
              :key="u.user_id"
              type="button"
              class="min-w-[200px] rounded-lg border px-3 py-2 text-left transition"
              :class="
                String(u.user_id) === filters.user_id
                  ? 'border-primary-500 bg-primary-50 dark:border-primary-500 dark:bg-primary-950/40'
                  : 'border-gray-200 bg-gray-50 hover:border-primary-400 dark:border-dark-700 dark:bg-dark-900'
              "
              :aria-pressed="String(u.user_id) === filters.user_id"
              @click="toggleUser(u.user_id)"
            >
              <span class="block text-sm font-semibold">{{ u.username || `#${u.user_id}` }}</span>
              <span class="mt-0.5 block text-xs text-gray-500 dark:text-dark-400">{{ u.user_email || '—' }}</span>
              <span class="mt-0.5 block text-xs text-gray-500 dark:text-dark-400">
                {{ t('admin.conversationArchive.users.summary', { c: u.conversations, r: u.requests, k: u.api_keys }) }}
              </span>
            </button>
          </div>
        </section>

        <div class="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
          <table class="w-full text-sm">
            <thead class="text-xs text-gray-500 dark:text-dark-400">
              <tr class="border-b border-gray-200 dark:border-dark-700">
                <th class="px-3 py-2 text-left">{{ t('admin.conversationArchive.table.lastActive') }}</th>
                <th class="px-3 py-2 text-left">{{ t('admin.conversationArchive.table.user') }}</th>
                <th class="px-3 py-2 text-left">{{ t('admin.conversationArchive.table.apiKey') }}</th>
                <th class="px-3 py-2 text-left">{{ t('admin.conversationArchive.table.route') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.conversationArchive.table.requests') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.conversationArchive.table.turns') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.conversationArchive.table.toolCalls') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.conversationArchive.table.chars') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="loading">
                <td colspan="8" class="px-3 py-8 text-center text-gray-500 dark:text-dark-400">
                  {{ t('admin.conversationArchive.loading') }}
                </td>
              </tr>
              <tr v-else-if="error">
                <td colspan="8" class="px-3 py-8 text-center text-red-600 dark:text-red-400">{{ error }}</td>
              </tr>
              <tr v-else-if="!conversations.length">
                <td colspan="8" class="px-3 py-8 text-center text-gray-500 dark:text-dark-400">
                  {{ t('admin.conversationArchive.empty') }}
                </td>
              </tr>
              <tr
                v-for="c in conversations"
                v-else
                :key="c.id"
                class="cursor-pointer border-b border-gray-100 last:border-0 hover:bg-primary-50 dark:border-dark-700 dark:hover:bg-dark-700/50"
                @click="openDetail(c.id)"
              >
                <td class="px-3 py-2 font-mono text-xs">
                  {{ formatDate(c.updated_at) }}
                  <span class="block text-gray-400 dark:text-dark-500">{{ formatDate(c.started_at) }}</span>
                </td>
                <td class="px-3 py-2">
                  <button
                    type="button"
                    class="text-primary-600 hover:underline dark:text-primary-400"
                    :title="t('admin.conversationArchive.users.only')"
                    @click.stop="toggleUser(c.user_id)"
                  >
                    {{ c.username || `#${c.user_id}` }}
                  </button>
                  <span class="block font-mono text-xs text-gray-400 dark:text-dark-500">{{ c.user_email }}</span>
                </td>
                <td class="px-3 py-2">
                  {{ c.api_key_name || '—' }}
                  <span class="block font-mono text-xs text-gray-400 dark:text-dark-500">#{{ c.api_key_id }}</span>
                </td>
                <td class="px-3 py-2">
                  {{ c.protocol }}
                  <span class="block font-mono text-xs text-gray-400 dark:text-dark-500">{{ c.model }}</span>
                </td>
                <td class="px-3 py-2 text-right tabular-nums">{{ c.requests }}</td>
                <td class="px-3 py-2 text-right tabular-nums">{{ c.turns }}</td>
                <td class="px-3 py-2 text-right tabular-nums">{{ c.tool_calls }}</td>
                <td class="px-3 py-2 text-right tabular-nums">
                  <span v-if="c.text_purged_at" class="text-gray-400 dark:text-dark-500">
                    {{ t('admin.conversationArchive.purged') }}
                  </span>
                  <span v-else>{{ c.chars.toLocaleString() }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="mt-3 flex items-center gap-3 text-sm text-gray-500 dark:text-dark-400">
          <button type="button" class="btn btn-secondary btn-sm" :disabled="offset <= 0" @click="prevPage">
            {{ t('admin.conversationArchive.actions.prev') }}
          </button>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="offset + pageSize >= total" @click="nextPage">
            {{ t('admin.conversationArchive.actions.next') }}
          </button>
          <span>{{ rangeLabel }}</span>
        </div>
      </template>
    </div>

    <ConversationDetailDialog v-model:open="detailOpen" :conversation-id="detailId" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import ConversationDetailDialog from './components/ConversationDetailDialog.vue'
import { getStatus, listConversations, listUsers } from './api'
import { emptyFilters, type ArchiveConversation, type ArchiveStats, type ArchiveStatus, type ArchiveUserSummary } from './types'

const { t } = useI18n()

const pageSize = 50
const filters = ref(emptyFilters())
const conversations = ref<ArchiveConversation[]>([])
const users = ref<ArchiveUserSummary[]>([])
const stats = ref<ArchiveStats | null>(null)
const status = ref<ArchiveStatus | null>(null)
const total = ref(0)
const offset = ref(0)
const loading = ref(true)
const error = ref('')
const usersCollapsed = ref(false)
const detailOpen = ref(false)
const detailId = ref<number | null>(null)

const userOptions = computed(() =>
  users.value.flatMap((u) => [u.username, u.user_email].filter(Boolean)),
)

const rangeLabel = computed(() => {
  const from = total.value === 0 ? 0 : offset.value + 1
  const to = Math.min(offset.value + pageSize, total.value)
  return t('admin.conversationArchive.range', { from, to, total: total.value.toLocaleString() })
})

function formatDate(value?: string): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

function formatBytes(n: number): string {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1)
  return `${(n / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

async function loadStatus() {
  try {
    status.value = await getStatus()
    if (status.value?.archive?.store) stats.value = status.value.archive.store
  } catch (err) {
    status.value = { enabled: true, reachable: false, error: (err as Error).message }
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const page = await listConversations(filters.value, pageSize, offset.value)
    conversations.value = page.items ?? []
    total.value = page.total
  } catch (err) {
    conversations.value = []
    error.value = (err as Error).message
  } finally {
    loading.value = false
  }
  try {
    users.value = await listUsers(filters.value)
  } catch {
    users.value = []
  }
}

function search() {
  offset.value = 0
  void load()
}

function reset() {
  filters.value = emptyFilters()
  offset.value = 0
  void load()
}

/** Clicking the active user clears the filter, so one control drills in and backs out. */
function toggleUser(userId: number) {
  filters.value.user_id = filters.value.user_id === String(userId) ? '' : String(userId)
  filters.value.user = ''
  offset.value = 0
  void load()
}

function prevPage() {
  offset.value = Math.max(0, offset.value - pageSize)
  void load()
}

function nextPage() {
  offset.value += pageSize
  void load()
}

function openDetail(id: number) {
  detailId.value = id
  detailOpen.value = true
}

onMounted(async () => {
  await loadStatus()
  if (status.value?.enabled && status.value.reachable !== false) await load()
  else loading.value = false
})
</script>
