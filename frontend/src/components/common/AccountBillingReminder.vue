<template>
  <div v-if="canShowBilling && billing">
    <div
      data-testid="account-billing-reminder"
      :aria-label="reminderLabel"
      :title="reminderLabel"
      class="flex max-w-full items-center gap-3 rounded-xl border px-4 py-1.5"
      :class="
        expiredAccountCount > 0
          ? 'border-red-200/70 bg-red-50/80 text-red-800 dark:border-red-800/60 dark:bg-red-900/20 dark:text-red-200'
          : billing.amount_due > 0
          ? 'border-amber-200/70 bg-amber-50/80 text-amber-800 dark:border-amber-800/60 dark:bg-amber-900/20 dark:text-amber-200'
          : 'border-primary-100 bg-primary-50/70 text-primary-700 dark:border-primary-900 dark:bg-primary-900/20 dark:text-primary-300'
      "
    >
      <Icon name="calendar" size="md" class="shrink-0" />
      <div class="min-w-0 text-center">
        <div class="flex flex-wrap items-baseline justify-center gap-x-2 text-xs">
          <span>{{ t('common.accountBillingReminder.monthlyDue') }}</span>
          <strong class="text-base font-semibold tabular-nums">¥{{ billing.amount_due.toFixed(2) }}</strong>
        </div>
        <p class="text-xs opacity-80">
          {{ billing.month }} · {{ renewalLabel }}
        </p>
        <p v-if="expiredAccountCount > 0" class="text-xs" data-testid="account-billing-expired">
          {{ expiryLabel }}
        </p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getUserBillingSummary, type UserBillingSummary } from '@/api/accountBilling'
import { useAuthStore } from '@/stores/auth'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const auth = useAuthStore()
const route = useRoute()
const canShowBilling = computed(
  () => auth.isAuthenticated && !auth.isAdmin && auth.user?.role === 'user',
)
const billing = ref<UserBillingSummary | null>(null)
let requestGeneration = 0

const expiredAccounts = computed(() => billing.value?.accounts.filter(account => account.expired === true && account.valid_through) ?? [])
const expiredAccountCount = computed(() => expiredAccounts.value.length)
const expiryLabel = computed(() => {
  const date = expiredAccounts.value.map(account => account.valid_through!.slice(0, 10)).sort()[0] ?? ''
  return t('common.accountBillingReminder.expiredAccounts', { count: expiredAccountCount.value, date })
})

// Keep the stored calendar date: parsing it as UTC can move the renewal day.
const earliestRenewal = computed(() =>
  billing.value?.accounts
    .filter((account) => account.payment_status !== 'paid' && account.price > 0)
    .map((account) => account.renewal_date?.slice(0, 10) || '')
    .filter((date) => /^\d{4}-\d{2}-\d{2}$/.test(date))
    .sort()[0],
)

const renewalLabel = computed(() => {
  if (!billing.value || billing.value.amount_due <= 0) {
    return t('common.accountBillingReminder.noPaymentDue')
  }
  return earliestRenewal.value
    ? t('common.accountBillingReminder.earliestRenewal', { date: earliestRenewal.value })
    : t('common.accountBillingReminder.dateNotSet')
})

const reminderLabel = computed(() =>
  `${billing.value?.month} ${t('common.accountBillingReminder.monthlyDue')} ¥${billing.value?.amount_due.toFixed(2)} · ${renewalLabel.value}${expiredAccountCount.value > 0 ? ' · ' + expiryLabel.value : ''}`,
)

async function load() {
  const generation = ++requestGeneration
  const userID = auth.user?.id
  billing.value = null
  if (!canShowBilling.value || !userID) return
  try {
    const result = await getUserBillingSummary()
    // A delayed response must never show another session's amount.
    if (
      generation === requestGeneration &&
      auth.user?.id === userID &&
      canShowBilling.value
    ) {
      billing.value = result
    }
  } catch {
    // A failed request must not look like a zero amount due.
  }
}

watch(
  () => [auth.user?.id, canShowBilling.value, route.fullPath],
  () => { void load() },
  { immediate: true },
)

onMounted(() => {
  window.addEventListener('focus', load)
  window.addEventListener('account-billing-paid', load)
})
onBeforeUnmount(() => {
  ++requestGeneration
  window.removeEventListener('focus', load)
  window.removeEventListener('account-billing-paid', load)
})
</script>
