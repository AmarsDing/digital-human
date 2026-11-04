<script setup lang="ts">
import { ref, watch } from 'vue'
import NtSelect from '../ui/NtSelect.vue'
import NtToggle from '../ui/NtToggle.vue'
import NtTextarea from '../ui/NtTextarea.vue'
import NtSecretText from '../ui/NtSecretText.vue'
import { millisecondsToSeconds, secondsToMilliseconds, type FieldSpec, type GroupSpec } from './schema'

const props = defineProps<{
  spec: GroupSpec
  values: Record<string, unknown>
}>()
const emit = defineEmits<{
  (e: 'update', key: string, value: unknown): void
  // 数字字段失焦校验结果：越界/非数字时 valid=false，父级据此禁保存。
  (e: 'validity', valid: boolean): void
}>()

const invalid = ref<Record<string, string>>({})

// 切组时重置校验态。
watch(
  () => props.spec.id,
  () => {
    invalid.value = {}
    emit('validity', true)
  }
)

function onBlur(col: FieldSpec, ev: Event): void {
  if (col.type !== 'number') return
  const v = Number((ev.target as HTMLInputElement).value)
  let msg = ''
  if (!Number.isFinite(v)) msg = '请输入数字'
  else if (col.min !== undefined && v < col.min) msg = `不能小于 ${col.min}`
  else if (col.max !== undefined && v > col.max) msg = `不能大于 ${col.max}`
  else if (col.unitConversion === 'ms-to-s') {
    try {
      secondsToMilliseconds(v)
    } catch {
      msg = '超出安全毫秒范围'
    }
  }
  const next = { ...invalid.value }
  if (msg) next[col.key] = msg
  else delete next[col.key]
  invalid.value = next
  emit('validity', Object.keys(next).length === 0)
}

function str(key: string): string {
  const v = props.values[key]
  return v === null || v === undefined ? '' : String(v)
}
function displayValue(col: FieldSpec): string {
  const raw = props.values[col.key]
  if (col.unitConversion !== 'ms-to-s' || raw === null || raw === undefined || raw === '') return str(col.key)
  const milliseconds = Number(raw)
  return Number.isFinite(milliseconds) ? String(millisecondsToSeconds(milliseconds)) : ''
}
function bool(key: string): boolean {
  return Boolean(props.values[key])
}
function field(col: FieldSpec): string {
  return col.type === 'textarea' ? 'field field--wide' : 'field'
}
function inputValue(col: FieldSpec, ev: Event): unknown {
  const value = (ev.target as HTMLInputElement).value
  if (col.type === 'number') return Number(value)
  if (col.format === 'csv') return value.split(',').map((item) => item.trim()).filter(Boolean)
  return value
}
function onInput(col: FieldSpec, ev: Event): void {
  if (col.type === 'number' && col.unitConversion === 'ms-to-s') {
    const seconds = Number((ev.target as HTMLInputElement).value)
    try {
      emit('update', col.key, secondsToMilliseconds(seconds))
    } catch {
      // 非有限值/超大值不污染 store；失焦时显示明确校验错误。
    }
    return
  }
  emit('update', col.key, inputValue(col, ev))
}
</script>

<template>
  <div class="settings-form">
    <p v-if="spec.note" class="settings-form__note">{{ spec.note }}</p>
    <div class="settings-form__grid">
      <div v-for="col in spec.fields" :key="col.key" :class="field(col)">
        <NtSelect
          v-if="col.type === 'select'"
          :label="col.label"
          :model-value="str(col.key)"
          :options="col.options || []"
          @update:model-value="emit('update', col.key, $event)"
        />
        <NtToggle
          v-else-if="col.type === 'toggle'"
          :label="col.label"
          :hint="col.hint"
          :model-value="bool(col.key)"
          @update:model-value="emit('update', col.key, $event)"
        />
        <NtTextarea
          v-else-if="col.type === 'textarea'"
          :label="col.label"
          :hint="col.hint"
          :model-value="str(col.key)"
          @update:model-value="emit('update', col.key, $event)"
        />
        <div v-else-if="col.type === 'secret'" class="field__secret">
          <span class="field__label">{{ col.label }}</span>
          <NtSecretText :value="str(col.key)" :label="col.hint" />
        </div>
        <div v-else-if="col.type === 'readonly'" class="field__readonly">
          <span class="field__label">{{ col.label }}</span>
          <span class="field__readonly-value">{{ str(col.key) || '—' }}</span>
          <router-link v-if="col.linkTo" class="field__link" :to="col.linkTo">{{ col.linkLabel || col.linkTo }}</router-link>
          <span v-if="col.hint" class="field__hint">{{ col.hint }}</span>
        </div>
        <div v-else-if="col.type === 'custom'" class="field--wide field__custom">
          <span class="field__label">{{ col.label }}</span>
          <slot name="custom" :field="col" />
        </div>
        <label v-else class="field__input">
          <span class="field__label">{{ col.label }}</span>
          <span class="field__control">
            <input
              :type="col.type === 'number' ? 'number' : 'text'"
              :value="displayValue(col)"
              :min="col.min"
              :max="col.max"
              :step="col.step"
              :aria-invalid="!!invalid[col.key]"
              :class="{ 'field__control--invalid': !!invalid[col.key] }"
              @input="onInput(col, $event)"
              @blur="onBlur(col, $event)"
            />
            <span v-if="col.suffix" class="field__suffix">{{ col.suffix }}</span>
          </span>
          <span v-if="invalid[col.key]" class="field__invalid" role="alert">{{ invalid[col.key] }}</span>
          <span v-else-if="col.hint" class="field__hint">{{ col.hint }}</span>
        </label>
      </div>
    </div>
  </div>
</template>

<style scoped>
.settings-form__note {
  margin: 0 0 12px;
  padding: 8px 10px;
  border-left: 2px solid var(--nt-accent-border);
  background: var(--nt-accent-soft);
  color: var(--nt-text-2);
  font-size: var(--nt-fs-caption);
  line-height: 1.5;
}
.settings-form__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px 16px;
}
.field--wide {
  grid-column: 1 / -1;
}
.field__label {
  display: block;
  margin-bottom: 6px;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.field__input {
  display: block;
  min-width: 0;
}
.field__control {
  display: flex;
  align-items: center;
  gap: 6px;
}
.field__control input {
  flex: 1;
  min-width: 0;
  height: 42px;
  padding: 0 10px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
}
.field__control input:focus {
  outline: none;
  border-color: var(--nt-accent-border);
  box-shadow: 0 0 0 2px var(--nt-focus-ring);
}
.field__suffix {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.field__hint {
  display: block;
  margin-top: 4px;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.field__control--invalid {
  border-color: var(--nt-danger-border) !important;
}
.field__invalid {
  display: block;
  margin-top: 4px;
  color: var(--nt-danger);
  font-size: var(--nt-fs-micro);
}
.field__secret,
.field__readonly {
  min-width: 0;
}
.field__readonly-value {
  color: var(--nt-text-2);
  font-family: var(--nt-font-mono);
  font-size: var(--nt-fs-caption);
  white-space: pre-wrap;
  word-break: break-all;
}
.field__link {
  display: inline-flex;
  align-items: center;
  min-height: 24px;
  color: var(--nt-accent);
  font-size: var(--nt-fs-micro);
  text-decoration: none;
}
.field__link:focus-visible {
  outline: 2px solid var(--nt-focus-ring);
  outline-offset: 2px;
  border-radius: var(--nt-radius-s);
}
</style>
