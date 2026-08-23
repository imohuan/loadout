 <script setup lang="ts">
 /**
  * AxImage — 懒加载图片组件（JS 版）
  * 支持 loading / loaded / error 三态，点击预览，hover 放大图标。
  */
 import { ref, computed, watch } from 'vue';

 const props = defineProps({
   src: { type: String, required: true },
   alt: { type: String, default: '' },
   previewList: { type: Array, default: null },
   previewIndex: { type: Number, default: 0 },
   objectFit: { type: String, default: 'cover' }, // 'cover' | 'contain'
   adaptiveAspect: { type: Boolean, default: false },
 });

 const imgLoading = computed(() =>
   props.src && props.src.startsWith('blob:') ? 'eager' : 'lazy',
 );

 const emit = defineEmits(['load', 'error', 'preview']);

 const loadState = ref('loading');
 const retryKey = ref(0);

 watch(() => props.src, () => {
   loadState.value = 'loading';
   retryKey.value = 0;
 });

 const imageSrc = computed(() => {
   if (!props.src) return props.src;
   if (props.src.startsWith('blob:')) return props.src;
   if (retryKey.value <= 0) return props.src;
   const sep = props.src.includes('?') ? '&' : '?';
   return `${props.src}${sep}_retry=${retryKey.value}`;
 });

 const imgKey = computed(() => {
   if (!props.src || !props.src.startsWith('blob:')) return props.src;
   return `${props.src}#${retryKey.value}`;
 });

 function handleLoad(e: Event) {
   loadState.value = 'loaded';
   emit('load', e);
 }

 function handleError(e: Event) {
   loadState.value = 'error';
   emit('error', e);
 }

 function handleClick() {
   if (loadState.value === 'loaded') {
     const list = props.previewList ?? [imageSrc.value];
     const index = props.previewList ? (props.previewIndex ?? 0) : 0;
     emit('preview', imageSrc.value, list, index);
   } else if (loadState.value === 'error') {
     loadState.value = 'loading';
     retryKey.value++;
   }
 }
 </script>

 <template>
   <div
     class="group relative w-full cursor-pointer overflow-hidden bg-gray-100"
     :class="[!adaptiveAspect ? 'h-full' : 'h-auto', adaptiveAspect && loadState !== 'loaded' ? 'aspect-square' : '']"
     @click="handleClick"
   >
     <div v-if="loadState === 'loading'"
       class="absolute inset-0 flex flex-col items-center justify-center text-gray-400">
       <span class="material-symbols-outlined mb-1 text-xl animate-spin">progress_activity</span>
       <span class="text-[10px]">加载中...</span>
     </div>

     <div v-else-if="loadState === 'error'"
       class="absolute inset-0 flex flex-col items-center justify-center text-red-500 cursor-pointer">
       <span class="material-symbols-outlined mb-1 text-xl">broken_image</span>
       <span class="text-[10px] font-medium">加载失败</span>
       <span class="mt-0.5 text-[9px] text-gray-400">点击重试</span>
     </div>

     <img
       :key="imgKey"
       :src="imageSrc"
       :alt="alt"
       class="w-full transition-all duration-300"
       :class="[
         !adaptiveAspect ? 'h-full' : 'h-auto',
         objectFit === 'cover' ? 'object-cover' : 'object-contain',
         loadState === 'loaded' ? 'opacity-100' : 'opacity-0',
       ]"
       :loading="imgLoading"
       @load="handleLoad"
       @error="handleError"
     />

     <div v-if="loadState === 'loaded'"
       class="pointer-events-none absolute inset-0 flex items-center justify-center bg-black/0 opacity-0 transition-all group-hover:bg-black/30 group-hover:opacity-100">
       <span class="material-symbols-outlined text-2xl text-white drop-shadow-md scale-75 transition-transform group-hover:scale-100">
         zoom_in
       </span>
     </div>
   </div>
 </template>
