<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold">售价与订阅代付者</h1>
          <p class="mt-1 text-sm text-gray-500">
            维护 AI 平台套餐的人民币售价，以及负责代付订阅的人员。
          </p>
        </div>
        <button class="btn btn-secondary" :disabled="loading" @click="load">
          刷新
        </button>
      </div>
      <div
        v-if="error"
        role="alert"
        class="rounded-xl bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300"
      >
        {{ error }}
      </div>
      <div v-if="loading" role="status" class="py-12 text-center text-gray-500">
        正在加载…
      </div>
      <template v-else>
        <section class="card overflow-hidden">
          <div
            class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 p-5 dark:border-dark-700"
          >
            <h2 class="font-semibold">AI 套餐售价</h2>
            <button class="btn btn-primary" @click="openPlan()">
              新增套餐
            </button>
          </div>
          <div class="p-4">
            <input
              v-model="planSearch"
              class="input sm:max-w-sm"
              aria-label="搜索平台或套餐"
              placeholder="搜索平台或套餐"
            />
          </div>
          <div class="overflow-x-auto">
            <table class="billing-table">
              <thead>
                <tr>
                  <th>AI 平台</th>
                  <th>套餐名字</th>
                  <th>人民币售价</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="plan in filteredPlans" :key="plan.id">
                  <td>{{ plan.platform }}</td>
                  <td class="font-medium">{{ plan.name }}</td>
                  <td>¥{{ money(plan.price) }}</td>
                  <td>
                    <div class="flex gap-4">
                      <button class="text-primary-600" @click="openPlan(plan)">
                        编辑</button
                      ><button
                        class="text-red-600"
                        @click="
                          pendingDelete = {
                            kind: 'plan',
                            id: plan.id,
                            name: plan.name,
                          }
                        "
                      >
                        删除
                      </button>
                    </div>
                  </td>
                </tr>
                <tr v-if="!filteredPlans.length">
                  <td colspan="4" class="py-10 text-center text-gray-500">
                    {{
                      planSearch ? '没有匹配的套餐' : '暂无套餐，请先新增套餐'
                    }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
        <section class="card overflow-hidden">
          <div
            class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 p-5 dark:border-dark-700"
          >
            <h2 class="font-semibold">订阅代付者</h2>
            <button class="btn btn-primary" @click="openPayer()">
              新增代付者
            </button>
          </div>
          <div class="p-4">
            <input
              v-model="payerSearch"
              class="input sm:max-w-sm"
              aria-label="搜索代付者"
              placeholder="搜索代付者名字"
            />
          </div>
          <div class="overflow-x-auto">
            <table class="billing-table">
              <thead>
                <tr>
                  <th>名字</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="payer in filteredPayers" :key="payer.id">
                  <td class="font-medium">{{ payer.name }}</td>
                  <td>
                    <div class="flex gap-4">
                      <button
                        class="text-primary-600"
                        @click="openPayer(payer)"
                      >
                        编辑</button
                      ><button
                        class="text-red-600"
                        @click="
                          pendingDelete = {
                            kind: 'payer',
                            id: payer.id,
                            name: payer.name,
                          }
                        "
                      >
                        删除
                      </button>
                    </div>
                  </td>
                </tr>
                <tr v-if="!filteredPayers.length">
                  <td colspan="2" class="py-10 text-center text-gray-500">
                    {{ payerSearch ? '没有匹配的代付者' : '暂无代付者' }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </template>
    </div>
    <BaseDialog
      :show="dialog !== null"
      :title="
        dialog === 'plan'
          ? editingId
            ? '编辑套餐'
            : '新增套餐'
          : editingId
            ? '编辑代付者'
            : '新增代付者'
      "
      @close="closeDialog"
    >
      <form id="billing-catalog-form" class="space-y-4" @submit.prevent="save">
        <label v-if="dialog === 'plan'" class="block text-sm"
          >AI 平台<input
            v-model.trim="form.platform"
            required
            maxlength="100"
            class="input mt-2"
            placeholder="例如 GPT、Claude"
        /></label>
        <label class="block text-sm"
          >{{ dialog === 'plan' ? '套餐名字' : '代付者名字'
          }}<input
            v-model.trim="form.name"
            required
            maxlength="100"
            class="input mt-2"
        /></label>
        <label v-if="dialog === 'plan'" class="block text-sm"
          >人民币售价<input
            v-model.number="form.price"
            required
            type="number"
            min="0"
            max="99999999"
            step="0.01"
            class="input mt-2"
        /></label>
      </form>
      <template #footer
        ><div class="flex justify-end gap-3">
          <button
            class="btn btn-secondary"
            :disabled="saving"
            @click="closeDialog"
          >
            取消</button
          ><button
            type="submit"
            form="billing-catalog-form"
            class="btn btn-primary"
            :disabled="saving"
          >
            {{ saving ? '保存中…' : '保存' }}
          </button>
        </div></template
      >
    </BaseDialog>
    <BaseDialog
      :show="pendingDelete !== null"
      title="确认删除"
      width="narrow"
      @close="!saving && (pendingDelete = null)"
    >
      <p class="text-sm">
        确认删除“{{ pendingDelete?.name }}”？已被账号使用的条目需先解除关联。
      </p>
      <template #footer
        ><div class="flex justify-end gap-3">
          <button
            class="btn btn-secondary"
            :disabled="saving"
            @click="pendingDelete = null"
          >
            取消</button
          ><button class="btn btn-danger" :disabled="saving" @click="remove">
            {{ saving ? '删除中…' : '删除' }}
          </button>
        </div></template
      >
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores/app'
import {
  getCatalog,
  createPlan,
  updatePlan,
  deletePlan,
  createPayer,
  updatePayer,
  deletePayer,
  type BillingPlan,
  type BillingPayer,
} from '@/api/accountBilling'

const app = useAppStore()
const plans = ref<BillingPlan[]>([])
const payers = ref<BillingPayer[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const planSearch = ref('')
const payerSearch = ref('')
const dialog = ref<'plan' | 'payer' | null>(null)
const editingId = ref<number | null>(null)
const pendingDelete = ref<{
  kind: 'plan' | 'payer'
  id: number
  name: string
} | null>(null)
const form = reactive({ platform: '', name: '', price: 0 })
const filteredPlans = computed(() =>
  plans.value.filter((p) =>
    `${p.platform} ${p.name}`
      .toLowerCase()
      .includes(planSearch.value.trim().toLowerCase()),
  ),
)
const filteredPayers = computed(() =>
  payers.value.filter((p) =>
    p.name.toLowerCase().includes(payerSearch.value.trim().toLowerCase()),
  ),
)
const money = (value: number) => Number(value).toFixed(2)
const message = (err: unknown) =>
  (err as { message?: string })?.message || '操作失败，请重试'
async function load() {
  loading.value = true
  error.value = ''
  try {
    const data = await getCatalog()
    plans.value = data.plans
    payers.value = data.payers
  } catch (err) {
    error.value = message(err)
  } finally {
    loading.value = false
  }
}
function openPlan(plan?: BillingPlan) {
  editingId.value = plan?.id ?? null
  Object.assign(form, {
    platform: plan?.platform ?? '',
    name: plan?.name ?? '',
    price: plan?.price ?? 0,
  })
  dialog.value = 'plan'
}
function openPayer(payer?: BillingPayer) {
  editingId.value = payer?.id ?? null
  Object.assign(form, { platform: '', name: payer?.name ?? '', price: 0 })
  dialog.value = 'payer'
}
function closeDialog() {
  if (!saving.value) dialog.value = null
}
async function save() {
  if (saving.value || !form.name.trim()) return
  if (
    dialog.value === 'plan' &&
    (!form.platform.trim() || !Number.isFinite(form.price) || form.price < 0)
  )
    return
  saving.value = true
  try {
    if (dialog.value === 'plan') {
      const body = { ...form }
      if (editingId.value) await updatePlan(editingId.value, body)
      else await createPlan(body)
    } else {
      const body = { name: form.name }
      if (editingId.value) await updatePayer(editingId.value, body)
      else await createPayer(body)
    }
    dialog.value = null
    app.showSuccess('保存成功')
    await load()
  } catch (err) {
    app.showError(message(err))
  } finally {
    saving.value = false
  }
}
async function remove() {
  if (!pendingDelete.value || saving.value) return
  saving.value = true
  try {
    const item = pendingDelete.value
    if (item.kind === 'plan') await deletePlan(item.id)
    else await deletePayer(item.id)
    pendingDelete.value = null
    app.showSuccess('删除成功')
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
