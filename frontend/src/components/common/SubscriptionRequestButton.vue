<template>
  <button
    v-if="canRequest"
    type="button"
    class="inline-flex shrink-0 items-center gap-1.5 rounded-lg bg-primary-50 px-2 py-1.5 font-medium text-primary-700 transition-colors hover:bg-primary-100 dark:bg-primary-900/20 dark:text-primary-300 dark:hover:bg-primary-900/40 sm:px-2.5"
    aria-label="申请订阅账号"
    data-testid="subscription-request-button"
    @click="openDialog"
  >
    <Icon name="userPlus" size="sm" class="shrink-0" />
    <span class="max-w-[4.5rem] text-xs leading-4 sm:max-w-none sm:text-sm">申请订阅账号</span>
  </button>
  <BaseDialog :show="opened && canRequest" title="申请订阅账号" @close="closeDialog">
    <div v-if="loading" class="py-8 text-center text-sm text-gray-500" role="status">正在加载…</div>
    <p v-if="error" class="mb-4 rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300" role="alert">{{ error }}</p>
    <template v-if="loaded && !loading">
      <div v-if="pending" class="mb-4 rounded-xl bg-gray-50 p-3 dark:bg-dark-900" data-testid="current-subscription-request">
        <div class="flex items-center justify-between gap-2">
          <span class="text-xs text-gray-500">当前申请</span>
          <span class="badge badge-warning">待处理</span>
        </div>
        <p class="mt-2 break-words text-sm font-medium">{{ pending.platform }} / {{ pending.plan_name }}</p>
        <p class="mt-1 text-sm tabular-nums">¥{{ money(pending.price) }}</p>
      </div>
      <form id="subscription-request-form" class="space-y-3" @submit.prevent="submit">
        <label class="block text-sm">
          {{ pending ? '修改申请套餐' : '申请套餐' }}
          <select v-model="planId" class="input mt-2" required :disabled="saving || !plans.length" aria-label="申请套餐">
            <option :value="null" disabled>{{ plans.length ? '请选择套餐' : '暂无可申请套餐' }}</option>
            <option v-for="plan in plans" :key="plan.id" :value="plan.id">{{ plan.platform }} / {{ plan.name }} · ¥{{ money(plan.price) }}</option>
          </select>
        </label>
      </form>
      <section v-if="completedRequests.length" class="mt-5 border-t border-gray-100 pt-4 dark:border-dark-700">
        <h3 class="mb-2 text-sm font-medium">申请记录</h3>
        <div class="max-h-60 space-y-2 overflow-y-auto">
          <div v-for="item in completedRequests" :key="item.id" class="rounded-lg border border-gray-100 p-3 text-sm dark:border-dark-700" data-testid="completed-subscription-request">
            <div class="flex items-start justify-between gap-3">
              <p class="min-w-0 break-words">{{ item.platform }} / {{ item.plan_name }}</p>
              <span class="badge badge-success shrink-0">已完成</span>
            </div>
            <div class="mt-1 flex flex-wrap items-center justify-between gap-2 text-xs text-gray-500">
              <span class="tabular-nums">¥{{ money(item.price) }}</span>
              <time :datetime="item.completed_at || item.created_at">{{ formatDate(item.completed_at || item.created_at) }}</time>
            </div>
          </div>
        </div>
      </section>
    </template>
    <template #footer>
      <div class="flex items-center justify-between gap-3">
        <button type="button" class="btn btn-secondary" :disabled="loading || saving" @click="load">刷新</button>
        <button
          v-if="loaded && !loading"
          class="btn btn-primary"
          type="submit"
          form="subscription-request-form"
          :disabled="saving || !selectedPlan || pending?.plan_id === planId"
          data-testid="subscription-request-submit"
        >{{ saving ? '保存中…' : pending ? '保存修改' : '提交申请' }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import {
  createSubscriptionRequest,
  getSubscriptionRequestPlans,
  getSubscriptionRequests,
  updateSubscriptionRequest,
  type SubscriptionRequest,
  type SubscriptionRequestPlan,
} from '@/api/subscriptionRequests'

const auth = useAuthStore()
const app = useAppStore()
const canRequest = computed(() => auth.isAuthenticated && !auth.isAdmin && auth.user?.role === 'user')
const opened = ref(false)
const loading = ref(false)
const loaded = ref(false)
const saving = ref(false)
const error = ref('')
const plans = ref<SubscriptionRequestPlan[]>([])
const requests = ref<SubscriptionRequest[]>([])
const planId = ref<number | null>(null)
const pending = computed(() => requests.value.find(item => item.status === 'pending'))
const completedRequests = computed(() => requests.value.filter(item => item.status === 'completed'))
const selectedPlan = computed(() => plans.value.find(plan => plan.id === planId.value))
let generation = 0

const money = (value: number) => Number(value).toFixed(2)
const formatDate = (value: string) => new Date(value).toLocaleString('zh-CN', { hour12: false })
const message = (err: unknown) => (err as { message?: string })?.message || '操作失败，请重试'

function closeDialog() {
  if (saving.value) return
  opened.value = false
  ++generation
}

async function openDialog() {
  if (!canRequest.value) return
  opened.value = true
  await load()
}

async function load() {
  if (!opened.value || !canRequest.value) return
  const current = ++generation
  const userId = auth.user?.id
  loading.value = true
  loaded.value = false
  error.value = ''
  try {
    const [catalog, history] = await Promise.all([getSubscriptionRequestPlans(), getSubscriptionRequests()])
    if (current !== generation || userId !== auth.user?.id || !canRequest.value) return
    plans.value = catalog.plans
    requests.value = history.items
    planId.value = pending.value?.plan_id ?? plans.value[0]?.id ?? null
    loaded.value = true
  } catch (err) {
    if (current === generation) error.value = message(err)
  } finally {
    if (current === generation) loading.value = false
  }
}

async function submit() {
  if (!canRequest.value || !opened.value || !loaded.value || loading.value || saving.value || !selectedPlan.value || pending.value?.plan_id === planId.value) return
  const current = generation
  const userId = auth.user?.id
  const existing = pending.value
  saving.value = true
  error.value = ''
  try {
    const result = existing
      ? await updateSubscriptionRequest(existing.id, selectedPlan.value.id)
      : await createSubscriptionRequest(selectedPlan.value.id)
    if (current !== generation || userId !== auth.user?.id || !canRequest.value) return
    requests.value = [result, ...requests.value.filter(item => item.id !== result.id)]
    planId.value = result.status === 'pending' ? result.plan_id : plans.value[0]?.id ?? null
    app.showSuccess(existing ? '申请已更新' : '申请已提交')
  } catch (err) {
    if (current !== generation) return
    const failure = message(err)
    if ((err as { status?: number })?.status === 409) await load()
    error.value = failure
  } finally {
    saving.value = false
  }
}

watch(() => [auth.user?.id, canRequest.value], () => {
  ++generation
  opened.value = false
  loaded.value = false
  loading.value = false
  saving.value = false
  plans.value = []
  requests.value = []
  planId.value = null
  error.value = ''
})
onBeforeUnmount(() => { ++generation })
</script>
