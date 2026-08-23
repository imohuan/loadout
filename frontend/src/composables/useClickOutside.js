import { onMounted, onBeforeUnmount } from "vue";

/**
 * 监听点击指定元素外部，触发回调
 * @param {import("vue").Ref<HTMLElement|null>} targetRef
 * @param {() => void} handler
 */
export function useClickOutside(targetRef, handler) {
  function onPointerDown(e) {
    const el = targetRef.value;
    if (!el) return;
    if (el.contains(e.target)) return;
    handler(e);
  }
  onMounted(() => document.addEventListener("pointerdown", onPointerDown));
  onBeforeUnmount(() => document.removeEventListener("pointerdown", onPointerDown));
}
