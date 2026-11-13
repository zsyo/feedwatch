<script setup lang="ts">
withDefaults(defineProps<{ kind?: 'error' | 'success' | 'info'; message?: string }>(), {
  kind: 'info',
  message: '',
})
</script>

<template>
  <div v-if="message" class="notice" :class="`notice-${kind}`" role="status">
    <span>{{ message }}</span>
    <slot />
  </div>
</template>
