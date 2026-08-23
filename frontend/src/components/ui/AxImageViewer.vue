 <script setup lang="ts">
 /**
  * AxImageViewer — 全屏图片查看器（JS 版）
  * 支持：缩放/旋转/翻转/左右切换/键盘快捷键/下载。
  */
 import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue';

 const props = withDefaults(
   defineProps<{
     images: string[]
     initialIndex?: number
     visible: boolean
   }>(),
   { initialIndex: 0 },
 );

 const emit = defineEmits<{
   'update:visible': [value: boolean]
   close: []
 }>();

 const currentIndex = ref(props.initialIndex ?? 0);
 const scale = ref(1);
 const rotation = ref(0);
 const flip = ref(false);
 const offsetX = ref(0);
 const offsetY = ref(0);
 const isDragging = ref(false);
 const dragStartX = ref(0);
 const dragStartY = ref(0);

 const currentImage = computed(() => props.images[currentIndex.value] ?? '');
 const isMultiple = computed(() => props.images.length > 1);

 const imageStyle = computed(() => ({
   transform: `translate(${offsetX.value}px, ${offsetY.value}px) scale(${scale.value}) rotate(${rotation.value}deg) scaleX(${flip.value ? -1 : 1})`,
   transition: isDragging.value ? 'none' : 'transform 0.2s ease',
 }));

 function close() {
   emit('update:visible', false);
   emit('close');
 }

 function prevImage() {
   if (!isMultiple.value) return;
   currentIndex.value = (currentIndex.value - 1 + props.images.length) % props.images.length;
   resetTransform();
 }

 function nextImage() {
   if (!isMultiple.value) return;
   currentIndex.value = (currentIndex.value + 1) % props.images.length;
   resetTransform();
 }

 function zoomIn() { scale.value = Math.min(scale.value + 0.25, 5); }
 function zoomOut() { scale.value = Math.max(scale.value - 0.25, 0.25); }
 function rotateImg() { rotation.value = (rotation.value + 90) % 360; }
 function toggleFlip() { flip.value = !flip.value; }
 function reset() {
   scale.value = 1; rotation.value = 0;
   flip.value = false; offsetX.value = 0; offsetY.value = 0;
 }
 function resetTransform() {
   scale.value = 1; rotation.value = 0;
   flip.value = false; offsetX.value = 0; offsetY.value = 0;
 }

 function handleWheel(e: WheelEvent) {
   e.preventDefault();
   scale.value = Math.min(Math.max(scale.value + (e.deltaY > 0 ? -0.1 : 0.1), 0.25), 5);
 }

 function handleMouseDown(e: MouseEvent) {
   if ((e.target as HTMLElement | null)?.closest('.iv-controls')) return;
   isDragging.value = true;
   dragStartX.value = e.clientX - offsetX.value;
   dragStartY.value = e.clientY - offsetY.value;
 }
 function handleMouseMove(e: MouseEvent) {
   if (!isDragging.value) return;
   offsetX.value = e.clientX - dragStartX.value;
   offsetY.value = e.clientY - dragStartY.value;
 }
 function handleMouseUp() { isDragging.value = false; }

 async function downloadImage() {
   if (!currentImage.value) return;
   try {
     const resp = await fetch(currentImage.value);
     const blob = await resp.blob();
     const url = URL.createObjectURL(blob);
     const a = document.createElement('a');
     a.href = url;
     a.download = `image_${currentIndex.value + 1}.jpg`;
     a.click();
     URL.revokeObjectURL(url);
   } catch {
     window.open(currentImage.value, '_blank');
   }
 }

 function handleKeydown(e: KeyboardEvent) {
   switch (e.key) {
     case 'Escape': close(); break;
     case 'ArrowLeft': case 'a': case 'A': prevImage(); break;
     case 'ArrowRight': case 'd': case 'D': nextImage(); break;
     case '+': case '=': zoomIn(); break;
     case '-': zoomOut(); break;
     case '0': reset(); break;
     case 'r': case 'R': rotateImg(); break;
     case 'f': case 'F': toggleFlip(); break;
   }
 }

 watch(() => props.visible, (v) => {
   if (v) {
     currentIndex.value = props.initialIndex ?? 0;
     reset();
     document.addEventListener('keydown', handleKeydown);
     document.body.style.overflow = 'hidden';
   } else {
     document.removeEventListener('keydown', handleKeydown);
     document.body.style.overflow = '';
   }
 });

 watch(() => props.initialIndex, (v) => { currentIndex.value = v ?? 0; });

 onMounted(() => {
   if (props.visible) {
     document.addEventListener('keydown', handleKeydown);
     document.body.style.overflow = 'hidden';
   }
 });

 onBeforeUnmount(() => {
   document.removeEventListener('keydown', handleKeydown);
   document.body.style.overflow = '';
 });
 </script>

 <template>
   <Teleport to="body">
     <div
       v-if="visible"
       class="iv-overlay"
       @click.self="close"
       @mousedown="handleMouseDown"
       @mousemove="handleMouseMove"
       @mouseup="handleMouseUp"
     >
       <button class="iv-close-btn" title="关闭 (Esc)" @click="close">
         <span class="material-symbols-outlined text-xl">close</span>
       </button>

       <div class="iv-container">
         <button v-if="isMultiple" class="iv-nav-btn iv-prev" title="上一张 (←)" @click.stop="prevImage">
           <span class="material-symbols-outlined">chevron_left</span>
         </button>

         <div class="iv-image-area" @wheel="handleWheel">
           <img
             v-if="currentImage"
             :src="currentImage"
             class="iv-image"
             :class="{ 'iv-drag': isDragging }"
             :style="imageStyle"
             draggable="false"
           />
         </div>

         <button v-if="isMultiple" class="iv-nav-btn iv-next" title="下一张 (→)" @click.stop="nextImage">
           <span class="material-symbols-outlined">chevron_right</span>
         </button>
       </div>

       <div class="iv-controls">
         <div class="iv-controls-inner">
           <template v-if="isMultiple">
             <span class="iv-page">{{ currentIndex + 1 }} / {{ images.length }}</span>
             <div class="iv-divider" />
           </template>

           <div class="iv-zoom-group">
             <button title="缩小 (-)" @click="zoomOut"><span class="material-symbols-outlined text-sm">zoom_out</span></button>
             <span class="iv-zoom-label">{{ Math.round(scale * 100) }}%</span>
             <button title="放大 (+)" @click="zoomIn"><span class="material-symbols-outlined text-sm">zoom_in</span></button>
           </div>

           <div class="iv-divider" />
           <button title="旋转 (R)" @click="rotateImg"><span class="material-symbols-outlined text-sm">rotate_right</span></button>
           <button title="翻转 (F)" @click="toggleFlip"><span class="material-symbols-outlined text-sm">swap_horiz</span></button>
           <button title="重置 (0)" @click="reset"><span class="material-symbols-outlined text-sm">undo</span></button>
           <div class="iv-divider" />
           <button title="下载" @click="downloadImage"><span class="material-symbols-outlined text-sm">download</span></button>
         </div>
       </div>
     </div>
   </Teleport>
 </template>

 <style scoped>
 .iv-overlay {
   position: fixed;
   inset: 0;
   z-index: 9999;
   display: flex;
   align-items: center;
   justify-content: center;
   background: rgba(0, 0, 0, 0.95);
   user-select: none;
 }
 .iv-close-btn {
   position: fixed;
   top: 24px;
   right: 24px;
   z-index: 10000;
   width: 40px;
   height: 40px;
   padding: 0;
   display: flex;
   align-items: center;
   justify-content: center;
   background: rgba(255, 255, 255, 0.1);
   border: 1px solid rgba(255, 255, 255, 0.1);
   border-radius: 8px;
   color: white;
   cursor: pointer;
   backdrop-filter: blur(10px);
   transition: all 0.2s ease;
 }
 .iv-close-btn:hover { background: rgba(255, 255, 255, 0.2); }
 .iv-container {
   position: relative;
   width: 100%;
   height: 100%;
   display: flex;
   align-items: center;
   justify-content: center;
 }
 .iv-nav-btn {
   position: absolute;
   z-index: 40;
   width: 44px;
   height: 44px;
   padding: 0;
   display: flex;
   align-items: center;
   justify-content: center;
   background: rgba(255, 255, 255, 0.05);
   border: none;
   border-radius: 8px;
   color: white;
   cursor: pointer;
   backdrop-filter: blur(4px);
   transition: all 0.2s ease;
 }
 .iv-nav-btn:hover { background: rgba(255, 255, 255, 0.15); }
 .iv-prev { left: 16px; }
 .iv-next { right: 16px; }
 .iv-image-area {
   width: 100%;
   height: 100%;
   display: flex;
   align-items: center;
   justify-content: center;
   overflow: hidden;
   cursor: grab;
 }
 .iv-image-area:active { cursor: grabbing; }
 .iv-image {
   max-height: 100vh;
   max-width: 100%;
   object-fit: contain;
   will-change: transform;
 }
 .iv-image.iv-drag { transition: none !important; }
 .iv-controls {
   position: absolute;
   bottom: 24px;
   left: 50%;
   transform: translateX(-50%);
   z-index: 50;
 }
 .iv-controls-inner {
   display: flex;
   align-items: center;
   gap: 2px;
   padding: 6px 12px;
   background: rgba(24, 24, 27, 0.9);
   border: 1px solid rgba(255, 255, 255, 0.1);
   border-radius: 12px;
   backdrop-filter: blur(20px);
   box-shadow: 0 20px 25px -5px rgba(0, 0, 0, 0.3);
 }
 .iv-page {
   font-size: 11px;
   font-weight: 600;
   color: #71717a;
   letter-spacing: 0.1em;
   padding: 0 6px;
   font-family: 'JetBrains Mono', monospace;
 }
 .iv-divider {
   width: 1px;
   height: 14px;
   background: rgba(255, 255, 255, 0.1);
   margin: 0 3px;
 }
 .iv-zoom-group {
   display: flex;
   align-items: center;
   gap: 2px;
   background: rgba(255, 255, 255, 0.05);
   border-radius: 6px;
   padding: 2px 4px;
 }
 .iv-zoom-label {
   min-width: 44px;
   text-align: center;
   font-size: 11px;
   color: white;
   font-family: 'JetBrains Mono', monospace;
 }
 .iv-controls-inner button {
   width: 28px;
   height: 28px;
   padding: 0;
   display: flex;
   align-items: center;
   justify-content: center;
   background: transparent;
   border: none;
   border-radius: 6px;
   color: white;
   cursor: pointer;
   transition: all 0.15s ease;
 }
 .iv-controls-inner button:hover { background: rgba(255, 255, 255, 0.1); color: #60a5fa; }
 </style>
