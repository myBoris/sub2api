<template>
  <div class="flex min-w-40 items-center gap-2">
    <span v-if="loading && !owner" class="text-xs text-gray-400">{{ t('common.loading') }}</span>
    <span v-else-if="error || !owner" class="text-xs text-red-500">{{ t('common.resourceOwnership.loadFailed') }}</span>
    <div v-else class="min-w-0 text-sm">
      <template v-if="owner.user_id !== null">
        <div class="truncate" :title="owner.email">{{ owner.username || owner.email || `#${owner.user_id}` }}</div>
        <div class="truncate text-xs text-gray-400">{{ owner.email }} · #{{ owner.user_id }}</div>
      </template>
      <span v-else class="text-xs text-gray-400">{{ t('common.resourceOwnership.unassigned') }}</span>
    </div>
    <button
      v-if="editable && owner && !error"
      type="button"
      class="rounded p-1 text-gray-400 hover:text-primary-500"
      :aria-label="t('common.resourceOwnership.edit')"
      @click.stop="openEditor"
    >
      <Icon name="edit" size="sm" />
    </button>
  </div>

  <BaseDialog
    v-if="editable && editing"
    :show="editing"
    :title="t('common.resourceOwnership.edit')"
    width="normal"
    :close-on-escape="!saving"
    :show-close-button="!saving"
    @close="closeEditor"
  >
    <label class="mb-2 block text-sm font-medium" :for="`resource-owner-${kind}-${resourceId}`">
      {{ t('common.resourceOwnership.owner') }}
    </label>
    <Select
      v-model="selectedUserId"
      :id="`resource-owner-${kind}-${resourceId}`"
      :options="userOptions"
      :disabled="saving"
      :loading="searching"
      :search-placeholder="t('common.resourceOwnership.searchPlaceholder')"
      :aria-label="t('common.resourceOwnership.owner')"
      remote
      searchable
      @search="searchUsers"
      @change="rememberSelection"
    />
    <p class="mt-3 text-xs text-gray-500 dark:text-dark-300">{{ t('common.resourceOwnership.transferHint') }}</p>
    <p v-if="searchError" role="alert" class="mt-3 text-sm text-red-500">{{ searchError }}</p>
    <p v-if="saveError" role="alert" class="mt-3 text-sm text-red-500">{{ saveError }}</p>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="saving" @click="closeEditor">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" :disabled="saving || selectedUserId === owner?.user_id" @click="save">
        {{ t(saving ? 'common.saving' : 'common.save') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { list as listUsers } from '@/api/admin/users'
import { updateResourceOwner, type ResourceKind, type ResourceOwner } from '@/api/resourceOwnership'
import { extractApiErrorMessage } from '@/utils/apiError'
import BaseDialog from './BaseDialog.vue'
import Select, { type SelectOption } from './Select.vue'
import Icon from '@/components/icons/Icon.vue'

const props = withDefaults(defineProps<{
  kind: ResourceKind
  resourceId: number
  owner?: ResourceOwner
  loading?: boolean
  error?: boolean
  editable?: boolean
}>(), { loading: false, error: false, editable: false })
const emit = defineEmits<{ updated: [owner: ResourceOwner]; editingChange: [editing: boolean] }>()
const { t } = useI18n()
const editing = ref(false)
const saving = ref(false)
const searching = ref(false)
const selectedUserId = ref<string | number | boolean | null>(null)
const foundUsers = ref<SelectOption[]>([])
const selectedUserOption = ref<SelectOption | null>(null)
const searchError = ref('')
const saveError = ref('')
let searchController: AbortController | undefined

const userOptions = computed<SelectOption[]>(() => {
  const options: SelectOption[] = [{ value: null, label: t('common.resourceOwnership.unassigned') }]
  const owner = props.owner
  if (owner?.user_id != null && !foundUsers.value.some(user => user.value === owner.user_id)) {
    options.push({ value: owner.user_id, label: `${owner.username || owner.email} · #${owner.user_id}` })
  }
  const selected = selectedUserOption.value
  if (selected?.value != null && !options.some(option => option.value === selected.value) && !foundUsers.value.some(user => user.value === selected.value)) {
    options.push(selected)
  }
  return [...options, ...foundUsers.value]
})

async function searchUsers(query: string) {
  if (!props.editable || !editing.value) return
  searchController?.abort()
  const current = new AbortController()
  searchController = current
  searching.value = true
  searchError.value = ''
  foundUsers.value = []
  try {
    const result = await listUsers(1, 20, {
      status: 'active', search: query.trim(), include_subscriptions: false
    }, { signal: current.signal })
    if (!current.signal.aborted && props.editable && editing.value) {
      foundUsers.value = result.items.map(user => ({
        value: user.id,
        label: `${user.username || user.email} · ${user.email} · #${user.id}`
      }))
    }
  } catch (error) {
    if (!current.signal.aborted) searchError.value = extractApiErrorMessage(error, t('common.resourceOwnership.searchFailed'))
  } finally {
    if (searchController === current) searching.value = false
  }
}

function openEditor() {
  if (!props.editable || !props.owner) return
  selectedUserId.value = props.owner.user_id
  selectedUserOption.value = null
  foundUsers.value = []
  searchError.value = ''
  saveError.value = ''
  editing.value = true
  emit('editingChange', true)
  void searchUsers('')
}

function rememberSelection(_value: string | number | boolean | null, option: SelectOption | null) {
  selectedUserOption.value = option
}

function closeEditor() {
  if (saving.value) return
  editing.value = false
  searchController?.abort()
  emit('editingChange', false)
}

async function save() {
  if (!props.editable || !editing.value || saving.value) return
  const userId = selectedUserId.value
  if (userId !== null && (typeof userId !== 'number' || !Number.isSafeInteger(userId) || userId <= 0)) return
  saving.value = true
  saveError.value = ''
  try {
    const owner = await updateResourceOwner(props.kind, props.resourceId, userId)
    emit('updated', owner)
    editing.value = false
    searchController?.abort()
    emit('editingChange', false)
  } catch (error) {
    saveError.value = extractApiErrorMessage(error, t('common.resourceOwnership.saveFailed'))
  } finally {
    saving.value = false
  }
}

watch(() => props.editable, editable => {
  if (!editable) {
    editing.value = false
    searchController?.abort()
    emit('editingChange', false)
  }
})
onUnmounted(() => {
  searchController?.abort()
  if (editing.value) emit('editingChange', false)
})
</script>
