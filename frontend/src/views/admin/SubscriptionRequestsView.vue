<template>
  <AppLayout>
    <div class="space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <h1 class="text-xl font-semibold">订阅账号申请</h1>
        <button class="btn btn-secondary" :disabled="loading || completingId !== null" @click="load">刷新</button>
      </div>
      <p v-if="error" class="rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300" role="alert">{{ error }}</p>
      <section class="card overflow-hidden">
        <div class="flex flex-wrap items-center gap-3 p-4">
          <input v-model="search" class="input sm:max-w-xs" placeholder="搜索申请人、平台或套餐" aria-label="搜索订阅账号申请" />
          <select v-model="status" class="input sm:max-w-40" aria-label="申请状态">
            <option value="">全部状态</option>
            <option value="pending">待处理</option>
            <option value="completed">已完成</option>
          </select>
          <span class="text-sm text-gray-500">共 {{ filteredItems.length }} 条申请</span>
        </div>
        <div class="overflow-x-auto">
          <table class="request-table">
            <thead><tr><th>申请人</th><th>平台 / 套餐</th><th>售价（人民币）</th><th>申请时间</th><th>状态</th><th>操作</th></tr></thead>
            <tbody>
              <tr v-if="loading"><td colspan="6" class="py-10 text-center text-gray-500" role="status">正在加载…</td></tr>
              <template v-else>
                <tr v-for="item in visibleItems" :key="item.id">
                  <td><p class="font-medium">{{ item.user_name || item.user_email }}</p><p v-if="item.user_email !== item.user_name" class="mt-1 text-xs text-gray-500">{{ item.user_email }}</p></td>
                  <td class="max-w-xs break-words">{{ item.platform }} / {{ item.plan_name }}</td>
                  <td class="whitespace-nowrap tabular-nums">¥{{ money(item.price) }}</td>
                  <td class="whitespace-nowrap"><time :datetime="item.created_at">{{ formatDate(item.created_at) }}</time></td>
                  <td><span class="badge whitespace-nowrap" :class="item.status === 'pending' ? 'badge-warning' : 'badge-success'">{{ item.status === 'pending' ? '待处理' : '已完成' }}</span></td>
                  <td>
                    <button v-if="item.status === 'pending'" class="whitespace-nowrap font-medium text-primary-600 disabled:opacity-50" :disabled="completingId !== null" @click="complete(item)">{{ completingId === item.id ? '处理中…' : '完成申请' }}</button>
                    <span v-else class="text-gray-400">—</span>
                  </td>
                </tr>
                <tr v-if="!filteredItems.length"><td colspan="6" class="py-10 text-center text-gray-500">{{ items.length ? '没有匹配的申请' : '暂无订阅账号申请' }}</td></tr>
              </template>
            </tbody>
          </table>
        </div>
        <div v-if="pageCount > 1" class="flex items-center justify-end gap-3 border-t border-gray-100 p-3 text-sm dark:border-dark-700">
          <button class="btn btn-secondary" :disabled="page === 1" @click="page--">上一页</button>
          <span>{{ page }} / {{ pageCount }}</span>
          <button class="btn btn-secondary" :disabled="page >= pageCount" @click="page++">下一页</button>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useAppStore } from '@/stores/app'
import { completeSubscriptionRequest, listAdminSubscriptionRequests, type AdminSubscriptionRequest, type SubscriptionRequestStatus } from '@/api/subscriptionRequests'

const app = useAppStore()
const items = ref<AdminSubscriptionRequest[]>([])
const loading = ref(false)
const completingId = ref<number | null>(null)
const error = ref('')
const search = ref('')
const status = ref<SubscriptionRequestStatus | ''>('')
const page = ref(1)
const pageSize = 20
const money = (value: number) => Number(value).toFixed(2)
const formatDate = (value: string) => new Date(value).toLocaleString('zh-CN', { hour12: false })
const message = (err: unknown) => (err as { message?: string })?.message || '操作失败，请重试'
const filteredItems = computed(() => items.value.filter(item =>
  (!status.value || item.status === status.value) &&
  `${item.user_name} ${item.user_email} ${item.platform} ${item.plan_name}`.toLowerCase().includes(search.value.trim().toLowerCase()),
))
const pageCount = computed(() => Math.max(1, Math.ceil(filteredItems.value.length / pageSize)))
const visibleItems = computed(() => filteredItems.value.slice((page.value - 1) * pageSize, page.value * pageSize))
watch([search, status], () => { page.value = 1 })

async function load() {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    items.value = (await listAdminSubscriptionRequests()).items
    page.value = Math.min(page.value, pageCount.value)
  } catch (err) {
    error.value = message(err)
  } finally {
    loading.value = false
  }
}

async function complete(item: AdminSubscriptionRequest) {
  if (loading.value || completingId.value !== null || item.status !== 'pending') return
  completingId.value = item.id
  error.value = ''
  try {
    const result = await completeSubscriptionRequest(item.id, item.updated_at)
    items.value = items.value.map(entry => entry.id === result.id ? result : entry)
    page.value = Math.min(page.value, pageCount.value)
    app.showSuccess('申请已完成')
  } catch (err) {
    if ((err as { status?: number })?.status === 409) {
      await load()
      error.value = '申请已变化，请刷新后重试'
    } else {
      error.value = message(err)
    }
  } finally {
    completingId.value = null
  }
}
onMounted(load)
</script>

<style scoped>
.request-table { @apply w-full text-left text-sm; }
.request-table th { @apply whitespace-nowrap bg-gray-50 px-4 py-3 font-medium text-gray-500 dark:bg-dark-900; }
.request-table td { @apply border-t border-gray-100 px-4 py-3 dark:border-dark-700; }
</style>
