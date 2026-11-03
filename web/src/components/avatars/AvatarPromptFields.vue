<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  GREETING_MAX,
  SYSTEM_PROMPT_MAX,
  validateGreeting,
  validateName,
  validateSlug,
  validateSystemPrompt
} from '../../utils/avatar-validate'

// slug / name / system_prompt / greeting 编辑（v3 §9.1 校验表逐字段落地）。
export interface AvatarFormValue {
  slug: string
  name: string
  system_prompt: string
  greeting: string
}

const props = defineProps<{
  modelValue: AvatarFormValue
  isNew: boolean
}>()
const emit = defineEmits<{
  (e: 'update:modelValue', v: AvatarFormValue): void
  (e: 'validity', valid: boolean): void
}>()

const touched = ref<Record<string, boolean>>({})

const errors = computed(() => ({
  slug: props.isNew ? validateSlug(props.modelValue.slug) : '',
  name: validateName(props.modelValue.name),
  system_prompt: validateSystemPrompt(props.modelValue.system_prompt),
  greeting: validateGreeting(props.modelValue.greeting)
}))

const valid = computed(() => !Object.values(errors.value).some(Boolean))
watch(valid, (v) => emit('validity', v), { immediate: true })

function set(key: keyof AvatarFormValue, v: string): void {
  touched.value = { ...touched.value, [key]: true }
  emit('update:modelValue', { ...props.modelValue, [key]: v })
}

function fieldErr(key: keyof AvatarFormValue): string {
  return touched.value[key] ? errors.value[key] : ''
}

function countOf(v: string): number {
  return [...v].length
}
</script>

<template>
  <section class="form" aria-label="档案信息">
    <label class="field">
      <span class="field__label">slug <i class="req" aria-hidden="true">*</i></span>
      <input
        type="text"
        :value="modelValue.slug"
        :disabled="!isNew"
        :aria-invalid="!!fieldErr('slug')"
        :class="{ 'field__input--invalid': !!fieldErr('slug') }"
        placeholder="小写字母/数字/中划线，1–32"
        @input="set('slug', ($event.target as HTMLInputElement).value)"
        @blur="touched = { ...touched, slug: true }"
      />
      <span v-if="fieldErr('slug')" class="field__err" role="alert">{{ fieldErr('slug') }}</span>
      <span v-else-if="!isNew" class="field__hint">slug 创建后不可修改</span>
    </label>

    <label class="field">
      <span class="field__label">名称 <i class="req" aria-hidden="true">*</i></span>
      <input
        type="text"
        :value="modelValue.name"
        :aria-invalid="!!fieldErr('name')"
        :class="{ 'field__input--invalid': !!fieldErr('name') }"
        :maxlength="20"
        @input="set('name', ($event.target as HTMLInputElement).value)"
      />
      <span v-if="fieldErr('name')" class="field__err" role="alert">{{ fieldErr('name') }}</span>
    </label>

    <label class="field field--wide">
      <span class="field__label">
        人设提示词
        <span class="mono">{{ countOf(modelValue.system_prompt) }}/{{ SYSTEM_PROMPT_MAX }}</span>
      </span>
      <textarea
        :value="modelValue.system_prompt"
        rows="6"
        :aria-invalid="!!fieldErr('system_prompt')"
        :class="{ 'field__input--invalid': !!fieldErr('system_prompt') }"
        @input="set('system_prompt', ($event.target as HTMLTextAreaElement).value)"
        @blur="touched = { ...touched, system_prompt: true }"
      />
      <span v-if="fieldErr('system_prompt')" class="field__err" role="alert">{{ fieldErr('system_prompt') }}</span>
    </label>

    <label class="field field--wide">
      <span class="field__label">
        开场白
        <span class="mono">{{ countOf(modelValue.greeting) }}/{{ GREETING_MAX }}</span>
      </span>
      <textarea
        :value="modelValue.greeting"
        rows="2"
        :aria-invalid="!!fieldErr('greeting')"
        :class="{ 'field__input--invalid': !!fieldErr('greeting') }"
        placeholder="开播成功后自动播报；留空不发送"
        @input="set('greeting', ($event.target as HTMLTextAreaElement).value)"
      />
      <span v-if="fieldErr('greeting')" class="field__err" role="alert">{{ fieldErr('greeting') }}</span>
    </label>
  </section>
</template>

<style scoped>
.form {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px 16px;
}
.field--wide {
  grid-column: 1 / -1;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}
.field__label {
  display: flex;
  justify-content: space-between;
  color: var(--nt-text-3);
  font-size: var(--nt-fs-caption);
}
.req {
  color: var(--nt-danger);
  font-style: normal;
}
.field input,
.field textarea {
  padding: 0 12px;
  border-radius: var(--nt-radius-m);
  border: 1px solid var(--nt-border-1);
  background: var(--nt-data-inset);
  color: var(--nt-text-0);
  font: inherit;
}
.field input {
  height: 42px;
}
.field textarea {
  padding: 10px 12px;
  resize: vertical;
  min-height: 44px;
}
.field input:focus,
.field textarea:focus {
  outline: none;
  border-color: var(--nt-accent-border);
  box-shadow: 0 0 0 2px var(--nt-focus-ring);
}
.field input:disabled {
  opacity: 0.6;
}
.field__input--invalid {
  border-color: var(--nt-danger-border) !important;
}
.field__err {
  color: var(--nt-danger);
  font-size: var(--nt-fs-micro);
}
.field__hint {
  color: var(--nt-text-3);
  font-size: var(--nt-fs-micro);
}
.mono {
  font-family: var(--nt-font-mono);
  font-variant-numeric: tabular-nums;
  font-size: var(--nt-fs-micro);
  color: var(--nt-text-3);
}
@media (max-width: 767px) {
  .form {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
