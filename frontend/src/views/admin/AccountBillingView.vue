<template>
  <AppLayout>
    <div class="space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold">账号支付状态</h1>
          <p class="mt-1 text-sm text-gray-500">
            {{ data?.month || '本月' }}
          </p>
        </div>
        <div class="flex gap-2">
          <button class="btn btn-secondary" :disabled="loading" @click="load">
            刷新</button
          ><button
            class="btn btn-primary"
            :disabled="syncing || loading"
            @click="sync"
          >
            {{ syncing ? '同步中…' : '同步账号' }}
          </button>
        </div>
      </div>
      <div
        v-if="error"
        role="alert"
        class="rounded-xl bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300"
      >
        {{ error }}
      </div>
      <section v-if="data" class="card overflow-hidden" aria-label="当月账单统计">
        <div class="grid grid-cols-2 gap-x-4 gap-y-3 p-4 sm:grid-cols-3 xl:grid-cols-6">
          <dl class="contents" data-testid="paid-summary">
            <div>
              <dt class="text-xs text-gray-500">已支付账号</dt>
              <dd class="mt-1 text-lg font-semibold tabular-nums text-emerald-600">
                {{ data.summary.paid_count }}
              </dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500">总金额</dt>
              <dd class="mt-1 break-all text-lg font-semibold tabular-nums">
                ¥{{ money(data.summary.paid_total_amount) }}
              </dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500">总成本</dt>
              <dd class="mt-1 break-all text-lg font-semibold tabular-nums">
                ¥{{ money(data.summary.paid_total_cost) }}
              </dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500">利润</dt>
              <dd
                class="mt-1 break-all text-lg font-semibold tabular-nums"
                :class="data.summary.paid_profit < 0 ? 'text-red-600 dark:text-red-400' : 'text-emerald-600'"
              >
                ¥{{ money(data.summary.paid_profit) }}
              </dd>
            </div>
          </dl>
          <dl class="contents">
            <div>
              <dt class="text-xs text-gray-500">未支付账号</dt>
              <dd class="mt-1 text-lg font-semibold tabular-nums text-amber-600">
                {{ data.summary.unpaid_count }}
              </dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500">3 天内到期</dt>
              <dd class="mt-1 text-lg font-semibold tabular-nums text-primary-600">
                {{ data.summary.due_soon_count }}
              </dd>
            </div>
          </dl>
        </div>
        <div class="flex items-start gap-3 border-t border-gray-100 px-4 py-2.5 dark:border-dark-700">
          <h2 class="shrink-0 py-1 text-xs text-gray-500">代付成本</h2>
          <div class="flex min-w-0 flex-wrap items-center gap-2">
            <span
              v-for="payer in data.summary.payers"
              :key="payer.payer_id ?? 'none'"
              class="inline-flex max-w-full items-center gap-2 rounded-md bg-gray-50 px-2.5 py-1 text-xs dark:bg-dark-900"
              data-testid="payer-cost-summary"
            >
              <span class="max-w-32 truncate text-gray-500" :title="payer.payer_name || '未指定代付者'">
                {{ payer.payer_name || '未指定代付者' }}
              </span>
              <span class="whitespace-nowrap font-medium tabular-nums">
                ¥{{ money(payer.total) }}
              </span>
            </span>
            <span v-if="!data.summary.payers.length" class="py-1 text-xs text-gray-500">
              暂无代付者统计
            </span>
          </div>
        </div>
      </section>
      <section class="card overflow-hidden">
        <div class="flex flex-wrap gap-3 p-4">
          <input
            v-model="search"
            class="input sm:max-w-xs"
            placeholder="搜索账号名称、平台或套餐"
            aria-label="搜索账号"
          />
          <select
            v-model="payerFilter"
            class="input sm:max-w-48"
            aria-label="按代付者筛选"
          >
            <option value="">全部代付者</option>
            <option value="none">未指定代付者</option>
            <option
              v-for="payer in catalog.payers"
              :key="payer.id"
              :value="String(payer.id)"
            >
              {{ payer.name }}
            </option>
          </select>
          <select
            v-model="statusFilter"
            class="input sm:max-w-44"
            aria-label="按支付状态筛选"
          >
            <option value="">全部支付状态</option>
            <option value="paid">当月已支付</option>
            <option value="unpaid">当月未支付</option>
          </select>
          <span class="self-center text-sm text-gray-500"
            >共 {{ filteredAccounts.length }} 个账号</span
          >
        </div>
        <div class="overflow-x-auto">
          <table class="billing-table">
            <thead>
              <tr>
                <th>账号名称</th>
                <th>续费时间</th>
                <th>AI 平台 / 套餐</th>
                <th>售价（人民币）</th>
                <th>成本（人民币）</th>
                <th>订阅代付者</th>
                <th>用户支付状态</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="loading">
                <td
                  colspan="8"
                  class="py-10 text-center text-gray-500"
                  role="status"
                >
                  正在加载…
                </td>
              </tr>
              <template v-else
                ><tr
                  v-for="account in visibleAccounts"
                  :key="account.account_id"
                >
                  <td class="max-w-xs break-words font-medium">
                    {{ account.account_name }}
                  </td>
                  <td class="whitespace-nowrap">
                    {{ account.renewal_date?.slice(0, 10) || '未设置' }}
                    <p
                      v-if="account.valid_through"
                      data-testid="billing-api-validity"
                      class="mt-1 text-xs"
                      :class="account.expired === true ? 'text-red-600 dark:text-red-400' : 'text-gray-500 dark:text-gray-400'"
                    >
                      {{ account.expired === true ? 'API已到期' : 'API未到期' }} · 有效期至 {{ account.valid_through.slice(0, 10) }}
                    </p>
                    <p v-else-if="account.valid_through === null" class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                      未设置 API 有效期
                    </p>
                  </td>
                  <td>
                    {{ account.platform || '未设置' }} /
                    {{ account.plan_name || '未选择套餐' }}
                  </td>
                  <td>¥{{ money(account.price) }}</td>
                  <td>¥{{ money(account.cost) }}</td>
                  <td>{{ account.payer_name || '未指定' }}</td>
                  <td>
                    <span
                      class="badge whitespace-nowrap"
                      :class="
                        account.payment_status === 'paid'
                          ? 'badge-success'
                          : 'badge-warning'
                      "
                      >{{
                        account.payment_status === 'paid'
                          ? '当月已支付'
                          : '当月未支付'
                      }}</span
                    >
                  </td>
                  <td>
                    <div class="flex whitespace-nowrap gap-4">
                      <button class="text-primary-600" @click="edit(account)">
                        编辑</button
                      ><button
                        class="text-primary-600"
                        @click="showHistory(account)"
                      >
                        支付记录
                      </button>
                    </div>
                  </td>
                </tr>
                <tr v-if="!filteredAccounts.length">
                  <td colspan="8" class="py-12 text-center text-gray-500">
                    {{
                      data?.items.length
                        ? '没有匹配的账号'
                        : '暂无支付账号，请先同步账号'
                    }}
                  </td>
                </tr></template
              >
            </tbody>
          </table>
        </div>
        <div
          v-if="filteredAccounts.length > pageSize"
          class="flex items-center justify-end gap-3 border-t border-gray-100 p-4 text-sm dark:border-dark-700"
        >
          <button
            class="btn btn-secondary"
            :disabled="page === 1"
            @click="page--"
          >
            上一页</button
          ><span>{{ page }} / {{ pageCount }}</span
          ><button
            class="btn btn-secondary"
            :disabled="page >= pageCount"
            @click="page++"
          >
            下一页
          </button>
        </div>
      </section>
    </div>
    <BaseDialog
      :show="editing !== null"
      :title="`编辑账号 · ${editing?.account_name || ''}`"
      @close="!saving && (editing = null)"
    >
      <form id="account-billing-edit" class="space-y-4" @submit.prevent="save">
        <label class="block text-sm"
          >续费时间<input
            v-model="form.renewal_date"
            type="date"
            class="input mt-2"
        /></label>
        <p class="text-xs text-gray-500 dark:text-dark-400">
          每到新月份按原续费日自动顺延；当月没有该日期时取月末，之后恢复原续费日。
        </p>
        <label class="block text-sm"
          >AI 平台 / 套餐<select
            v-model="form.plan_id"
            class="input mt-2"
            @change="applyPlan"
          >
            <option :value="null">未选择套餐</option>
            <option
              v-for="plan in catalog.plans"
              :key="plan.id"
              :value="plan.id"
            >
              {{ plan.platform }} / {{ plan.name }} · ¥{{ money(plan.price) }}
            </option>
          </select></label
        >
        <label class="block text-sm"
          >售价（人民币）<input
            v-model.number="form.price"
            type="number"
            min="0"
            max="99999999"
            step="0.01"
            required
            class="input mt-2"
          /><span class="mt-1 block text-xs text-gray-500"
            >选择套餐会带入售价和成本，可按账号调整。</span
          ></label
        >
        <label class="block text-sm"
          >成本（人民币）<input
            v-model.number="form.cost"
            type="number"
            min="0"
            max="99999999"
            step="0.01"
            required
            class="input mt-2"
        /></label>
        <label class="block text-sm"
          >订阅代付者<select v-model="form.payer_id" class="input mt-2">
            <option :value="null">未指定</option>
            <option
              v-for="payer in catalog.payers"
              :key="payer.id"
              :value="payer.id"
            >
              {{ payer.name }}
            </option>
          </select></label
        >
        <label class="block text-sm"
          >{{ data?.month }} 支付状态<select
            v-model="form.payment_status"
            class="input mt-2"
          >
            <option value="unpaid">当月未支付</option>
            <option value="paid">当月已支付</option>
          </select></label
        >
      </form>
      <template #footer
        ><div class="flex justify-end gap-3">
          <button
            class="btn btn-secondary"
            :disabled="saving"
            @click="editing = null"
          >
            取消</button
          ><button
            class="btn btn-primary"
            type="submit"
            form="account-billing-edit"
            :disabled="saving"
          >
            {{ saving ? '保存中…' : '保存' }}
          </button>
        </div></template
      >
    </BaseDialog>
    <BaseDialog
      :show="historyAccount !== null"
      :title="`支付记录 · ${historyAccount?.account_name || ''}`"
      width="wide"
      @close="!saving && (historyAccount = null)"
    >
      <div class="overflow-x-auto">
        <table class="billing-table">
          <thead>
            <tr>
              <th>年月</th>
              <th>支付状态</th>
              <th>金额（人民币）</th>
              <th>成本（人民币）</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="entry in sortedHistory" :key="entry.month">
              <td>{{ entry.month }}</td>
              <td>{{ entry.status === 'paid' ? '已支付' : '未支付' }}</td>
              <td>¥{{ money(entry.amount) }}</td>
              <td>¥{{ money(entry.cost) }}</td>
              <td>
                <button
                  class="text-primary-600"
                  @click="Object.assign(historyForm, entry)"
                >
                  修订
                </button>
              </td>
            </tr>
            <tr v-if="!sortedHistory.length">
              <td colspan="5" class="py-8 text-center text-gray-500">
                暂无历史记录
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <form
        class="mt-6 space-y-4 rounded-xl bg-gray-50 p-4 dark:bg-dark-900"
        @submit.prevent="saveHistory"
      >
        <h3 class="font-medium">补录 / 修订月度记录</h3>
        <div class="grid gap-3 sm:grid-cols-2">
          <label class="block text-sm"
            >年月<input
              v-model="historyForm.month"
              type="month"
              :max="data?.month"
              required
              class="input mt-2"
              @change="applyHistoryMonth" /></label
          ><label class="block text-sm"
            >状态<select v-model="historyForm.status" class="input mt-2">
              <option value="paid">已支付</option>
              <option value="unpaid">未支付</option>
            </select></label
          ><label class="block text-sm"
            >金额<input
              v-model.number="historyForm.amount"
              type="number"
              min="0"
              max="99999999"
              step="0.01"
              required
              class="input mt-2"
          /></label>
          <label class="block text-sm"
            >成本<input
              v-model.number="historyForm.cost"
              type="number"
              min="0"
              max="99999999"
              step="0.01"
              required
              class="input mt-2"
          /></label>
        </div>
        <p class="text-xs text-gray-500">
          相同年月会更新原有记录；修订当月会同步本月支付状态。
        </p>
        <button class="btn btn-primary" :disabled="saving">
          {{ saving ? '保存中…' : '保存支付记录' }}
        </button>
      </form>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores/app'
import {
  getCatalog,
  listBillingAccounts,
  syncBillingAccounts,
  updateBillingAccount,
  updateBillingHistory,
  type BillingAccount,
  type BillingAccountList,
  type BillingCatalog,
} from '@/api/accountBilling'
const app = useAppStore()
const data = ref<BillingAccountList | null>(null)
const catalog = ref<BillingCatalog>({ plans: [], payers: [] })
const loading = ref(false)
const syncing = ref(false)
const saving = ref(false)
const error = ref('')
const search = ref('')
const payerFilter = ref('')
const statusFilter = ref('')
const page = ref(1)
const pageSize = 30
const editing = ref<BillingAccount | null>(null)
const originalRenewalDate = ref('')
const historyAccount = ref<BillingAccount | null>(null)
const form = reactive({
  renewal_date: '',
  plan_id: null as number | null,
  price: 0,
  cost: 0,
  payer_id: null as number | null,
  payment_status: 'unpaid' as 'paid' | 'unpaid',
})
const historyForm = reactive({
  month: '',
  status: 'paid' as 'paid' | 'unpaid',
  amount: 0,
  cost: 0,
})
const money = (value: number) => Number(value).toFixed(2)
const message = (err: unknown) =>
  (err as { message?: string })?.message || '操作失败，请重试'
const filteredAccounts = computed(() =>
  (data.value?.items ?? []).filter(
    (a) =>
      (!search.value.trim() ||
        `${a.account_name} ${a.platform} ${a.plan_name ?? ''}`
          .toLowerCase()
          .includes(search.value.trim().toLowerCase())) &&
      (!payerFilter.value ||
        (payerFilter.value === 'none'
          ? !a.payer_id
          : a.payer_id === Number(payerFilter.value))) &&
      (!statusFilter.value || a.payment_status === statusFilter.value),
  ),
)
const pageCount = computed(() =>
  Math.max(1, Math.ceil(filteredAccounts.value.length / pageSize)),
)
const visibleAccounts = computed(() =>
  filteredAccounts.value.slice(
    (page.value - 1) * pageSize,
    page.value * pageSize,
  ),
)
const sortedHistory = computed(() =>
  [...(historyAccount.value?.history ?? [])].sort((a, b) =>
    b.month.localeCompare(a.month),
  ),
)
watch([search, payerFilter, statusFilter], () => {
  page.value = 1
})
async function load() {
  loading.value = true
  error.value = ''
  try {
    const [accounts, options] = await Promise.all([
      listBillingAccounts(),
      getCatalog(),
    ])
    data.value = accounts
    catalog.value = options
    page.value = Math.min(page.value, pageCount.value)
    if (historyAccount.value)
      historyAccount.value =
        accounts.items.find(
          (a) => a.account_id === historyAccount.value?.account_id,
        ) ?? null
  } catch (err) {
    error.value = message(err)
  } finally {
    loading.value = false
  }
}
async function sync() {
  syncing.value = true
  try {
    await syncBillingAccounts()
    app.showSuccess('账号同步完成，已有配置已保留')
    await load()
  } catch (err) {
    app.showError(message(err))
  } finally {
    syncing.value = false
  }
}
function edit(account: BillingAccount) {
  editing.value = account
  originalRenewalDate.value = account.renewal_date?.slice(0, 10) ?? ''
  Object.assign(form, {
    renewal_date: originalRenewalDate.value,
    plan_id: account.plan_id,
    price: account.price,
    cost: account.cost,
    payer_id: account.payer_id,
    payment_status: account.payment_status,
  })
}
function applyPlan() {
  const plan = catalog.value.plans.find((p) => p.id === form.plan_id)
  if (plan) {
    form.price = plan.price
    form.cost = plan.cost
  }
}
async function save() {
  if (
    !editing.value ||
    saving.value ||
    !Number.isFinite(form.price) ||
    form.price < 0 ||
    !Number.isFinite(form.cost) ||
    form.cost < 0
  )
    return
  saving.value = true
  try {
    await updateBillingAccount(editing.value.account_id, {
      ...form,
      renewal_date: form.renewal_date || null,
      renewal_date_changed: form.renewal_date !== originalRenewalDate.value,
    })
    editing.value = null
    app.showSuccess('账号支付信息已保存')
    await load()
  } catch (err) {
    app.showError(message(err))
  } finally {
    saving.value = false
  }
}
function showHistory(account: BillingAccount) {
  historyAccount.value = account
  historyForm.month = data.value?.month ?? ''
  applyHistoryMonth()
}
function applyHistoryMonth() {
  const account = historyAccount.value
  if (!account) return
  const current = account.history.find(entry => entry.month === historyForm.month)
  Object.assign(historyForm, {
    status: current?.status ?? account.payment_status,
    amount: current?.amount ?? account.price,
    cost: current?.cost ?? account.cost,
  })
}
async function saveHistory() {
  if (
    !historyAccount.value ||
    saving.value ||
    !historyForm.month ||
    !Number.isFinite(historyForm.amount) ||
    historyForm.amount < 0 ||
    !Number.isFinite(historyForm.cost) ||
    historyForm.cost < 0
  )
    return
  saving.value = true
  try {
    await updateBillingHistory(
      historyAccount.value.account_id,
      historyForm.month,
      { status: historyForm.status, amount: historyForm.amount, cost: historyForm.cost },
    )
    app.showSuccess('支付记录已保存')
    await load()
  } catch (err) {
    app.showError(message(err))
  } finally {
    saving.value = false
  }
}
onMounted(load)
</script>

<style scoped>
.billing-table {
  @apply w-full text-left text-sm;
}
.billing-table th {
  @apply whitespace-nowrap bg-gray-50 px-5 py-3 font-medium text-gray-500 dark:bg-dark-900;
}
.billing-table td {
  @apply border-t border-gray-100 px-5 py-4 dark:border-dark-700;
}
</style>
