<template>
  <main class="observatory">
    <div class="shell">
      <header class="topbar">
        <a href="/" class="wordmark"
          ><span class="brand-mark" aria-hidden="true">◈</span> 能力观测站
          <span class="wordmark-en">/ OBSERVATORY</span></a
        >
        <div class="topbar-right">
          <span class="sampling"><i></i> 定时采样</span
          ><button
            class="button"
            :disabled="loading || timelineLoading"
            @click="refreshAll"
          >
            ↻ <span>刷新数据</span>
          </button>
        </div>
      </header>

      <section class="intro">
        <div>
          <p class="eyebrow">MODEL INTELLIGENCE · OPEN OBSERVATION</p>
          <h1>
            让模型的表现，<br class="mobile-break" /><span>有迹可循。</span>
          </h1>
          <p class="intro-copy">
            从一道推理题，到一幅独立创作。用持续的观测记录变化，而不是凭印象判断。
          </p>
        </div>
        <div class="intro-note">
          <span class="note-number">01 / 02</span>
          <p>推理探测 <span>＋</span> 视觉作品</p>
          <small>两条独立观测链路，不混用评分</small>
        </div>
      </section>

      <section class="metrics" aria-label="观测概览">
        <div class="metric">
          <span>所选窗口 · 答对比例</span
          ><strong
            >{{ healthyPercent
            }}<small v-if="timeline && timeline.total">%</small></strong
          >
          <p>
            {{ rangeLabel }}内 {{ timeline?.total ?? "—" }} 次探测，含未判定结果
          </p>
        </div>
        <div class="metric">
          <span>所选窗口 · 答错探测</span
          ><strong :class="{ 'text-alert': timeline?.degraded }"
            >{{ timeline?.degraded ?? "—" }}<small>次</small></strong
          >
          <p>单题结果用于观测，不代表完整能力评估</p>
        </div>
        <div class="metric">
          <span>公开创作档案</span
          ><strong>{{ page?.total ?? "—" }}<small>幅</small></strong>
          <p>
            {{
              page?.enabled
                ? "来自开启检测与作品展示的分组"
                : page
                  ? "当前未开启公开作品生成"
                  : "正在读取作品状态"
            }}
          </p>
        </div>
      </section>

      <section class="chart-panel">
        <div v-if="timelineError" class="notice error" role="alert">
          {{ timelineError }} <button @click="loadTimeline">重试时间轴</button>
        </div>
        <DegradationTimelineChart
          v-model:range="rangeHours"
          :timeline="timeline"
          :loading="timelineLoading"
        />
        <p v-if="timeline?.reset_at" class="artwork-explanation mt-4" role="status">
          统计起点：{{ formatClock(timeline.reset_at) }} · 仅统计此后新入队的探测，历史作品不受影响。
        </p>
      </section>

      <section class="gallery" aria-labelledby="gallery-heading">
        <header class="section-heading">
          <div>
            <p class="eyebrow">THE CREATIVE ARCHIVE</p>
            <h2 id="gallery-heading">
              一题之外，看见创造力<span class="count">{{ total }}</span>
            </h2>
            <p>鹈鹕骑行 · 独立 SVG 创作 · 各分组按自己的模型与间隔生成</p>
          </div>
          <span class="archive-note">每一幅，都是一次独立作答 ↗</span>
        </header>
        <p class="artwork-explanation">
          新作品主题：鹈鹕骑自行车的 2D 动画（HTML + SVG）。缩略图保持静止，点击查看后播放动画；历史静态作品仍以图片展示，不按分组标准答案评分。
        </p>
        <div v-if="pageError" class="notice error" role="alert">
          {{ pageError }} <button @click="loadPage">重新加载</button>
        </div>
        <div
          v-if="loading && !works.length"
          class="art-grid"
          aria-label="作品加载中"
        >
          <div v-for="n in 6" :key="n" class="skeleton"></div>
        </div>
        <div v-else-if="!works.length && !pageError" class="empty-state">
          <span aria-hidden="true">◇</span>
          <h3>
            {{
              page?.enabled ? "下一幅作品，正在等待诞生" : "作品档案暂未开放"
            }}
          </h3>
          <p>
            {{
              page?.enabled
                ? "当前还没有可展示的作品；生成成功后会自动更新。"
                : "管理员开启分组的降智检测与作品展示后，创作将在这里呈现。"
            }}
          </p>
        </div>
        <div v-else class="art-grid" :aria-busy="loading">
          <article
            v-for="(work, index) in works"
            :key="work.id"
            class="art-card"
          >
            <button
              class="art-frame"
              :aria-label="`查看作品 ${work.id}，打开动画预览`"
              @click="openLightbox(work.id)"
            >
              <span class="art-index">{{
                String((currentPage - 1) * PAGE_SIZE + index + 1).padStart(
                  2,
                  "0",
                )
              }}</span>
              <img
                v-if="work.has_image && !imageErrors.has(work.id)"
                :src="imageURL(work.id)"
                :alt="`鹈鹕骑行，作品 ${work.id}`"
                loading="lazy"
                @error="markImageError(work.id)"
              />
              <span v-else class="image-fallback"
                >图片暂未加载 <span>点击查看或刷新重试</span></span
              >
              <span class="expand-mark" aria-hidden="true">▷</span>
            </button>
            <div class="art-meta">
              <div>
                <h3 :title="work.model">{{ work.model }}</h3>
                <p>
                  {{ work.reasoning_effort || "默认强度" }} <span>·</span>
                  {{ formatDuration(work.duration_ms) }}
                </p>
              </div>
              <span class="file-badge">SVG</span>
            </div>
            <div class="art-footer">
              <time :datetime="work.finished_at || work.created_at">{{
                formatClock(work.finished_at || work.created_at)
              }}</time
              ><span>#{{ work.id }}</span>
            </div>
          </article>
        </div>
        <nav v-if="total > PAGE_SIZE" class="pagination" aria-label="作品分页">
          <button
            class="button"
            :disabled="loading || currentPage <= 1"
            @click="currentPage--"
          >
            ← 上一页</button
          ><span>{{ currentPage }} / {{ pageCount }}</span
          ><button
            class="button"
            :disabled="loading || currentPage >= pageCount"
            @click="currentPage++"
          >
            下一页 →
          </button>
        </nav>
      </section>
      <footer class="footer">
        <span>OBSERVATORY <b>·</b> 记录表现，不定义能力。</span
        ><span
          >最近探测 {{ formatClock(timeline?.last_probe_at) }} ·
          时间为本地时区</span
        >
      </footer>
    </div>
    <dialog
      ref="lightbox"
      class="lightbox"
      aria-labelledby="art-dialog-title"
      @close="onDialogClose"
      @click="onBackdropClick"
    >
      <div class="lightbox-top">
        <h2 id="art-dialog-title">
          独立创作 <span>#{{ lightboxId }}</span>
        </h2>
        <button class="button" autofocus @click="closeLightbox">关闭 ×</button>
      </div>
      <p v-if="animationLoading" class="notice" role="status">正在加载动画…</p>
      <p v-else-if="animationError" class="notice" role="alert">{{ animationError }}</p>
      <p v-else-if="lightboxId" class="animation-caption" role="status">
        {{ animationAvailable ? (animationPaused ? "已暂停 · 点击播放将从头开始" : "动画播放中 · 关闭窗口即停止") : "这幅作品没有可播放的 SVG / CSS 动画，展示静态图片。" }}
      </p>
      <iframe
        v-if="lightboxId && animationDocument && animationAvailable && !animationPaused"
        class="animation-frame"
        :srcdoc="animationDocument"
        sandbox=""
        referrerpolicy="no-referrer"
        :title="`作品 ${lightboxId} 动画预览`"
        tabindex="-1"
      ></iframe>
      <img
        v-else-if="lightboxId && !imageErrors.has(lightboxId)"
        :src="imageURL(lightboxId)"
        :alt="`作品 ${lightboxId}`"
        @error="markImageError(lightboxId)"
      />
      <p v-else class="notice">
        图片加载失败或已被移除，请关闭后刷新作品列表。
      </p>
      <button v-if="animationAvailable && !animationLoading" class="button animation-toggle" type="button"
        :aria-pressed="!animationPaused" @click="animationPaused = !animationPaused">
        {{ animationPaused ? "播放动画" : "暂停动画" }}
      </button>
      <a
        v-if="lightboxId"
        :href="imageURL(lightboxId)"
        target="_blank"
        rel="noopener noreferrer"
        class="original-link"
        >在新窗口查看静态原图 ↗</a
      >
    </dialog>
  </main>
</template>

<script setup lang="ts">
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from "vue";
import DegradationTimelineChart from "./DegradationTimeline.vue";
import {
  publicImageURL,
  publicAnimation,
  publicPage,
  publicTimeline,
  type DegradationPublicPage,
  type DegradationTimeline,
} from "@/api/degradation";

const PAGE_SIZE = 12;
const page = ref<DegradationPublicPage | null>(null);
const timeline = ref<DegradationTimeline | null>(null);
const works = computed(() => page.value?.items ?? []);
const total = computed(() => page.value?.total ?? 0);
const loading = ref(false);
const timelineLoading = ref(false);
const pageError = ref("");
const timelineError = ref("");
const rangeHours = ref(24);
const currentPage = ref(1);
const pageCount = computed(() =>
  Math.max(1, Math.ceil(total.value / PAGE_SIZE)),
);
const rangeLabel = computed(() =>
  rangeHours.value === 24 ? "24 小时" : `${rangeHours.value / 24} 天`,
);
const healthyPercent = computed(() =>
  timeline.value?.total
    ? ((timeline.value.correct / timeline.value.total) * 100).toFixed(1)
    : "—",
);
const imageErrors = ref(new Set<number>());
const lightbox = ref<HTMLDialogElement | null>(null);
const lightboxId = ref(0);
const animationDocument = ref("");
const animationAvailable = ref(false);
const animationLoading = ref(false);
const animationError = ref("");
const animationPaused = ref(false);
let animationController: AbortController | undefined;
let animationSequence = 0;
let oldOverflow = "";
let focusBeforeDialog: HTMLElement | null = null;
let timer: ReturnType<typeof setInterval> | undefined;
let pageController: AbortController | undefined;
let timelineController: AbortController | undefined;
let pageSequence = 0;
let timelineSequence = 0;
let disposed = false;

const imageURL = (id: number) => publicImageURL(id);
const formatClock = (raw?: string | null) =>
  raw && !Number.isNaN(Date.parse(raw))
    ? new Date(raw).toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "—";
const formatDuration = (ms: number) =>
  ms > 0 ? `${(ms / 1000).toFixed(1)} 秒` : "耗时未知";
function markImageError(id: number) {
  imageErrors.value = new Set([...imageErrors.value, id]);
}

async function loadPage() {
  const sequence = ++pageSequence;
  pageController?.abort();
  pageController = new AbortController();
  loading.value = true;
  pageError.value = "";
  try {
    const result = await publicPage(
      currentPage.value,
      PAGE_SIZE,
      pageController.signal,
    );
    if (disposed || sequence !== pageSequence) return;
    const lastPage = Math.max(1, Math.ceil(result.total / PAGE_SIZE));
    if (currentPage.value > lastPage) {
      currentPage.value = lastPage;
      return;
    }
    page.value = result;
    imageErrors.value = new Set();
  } catch {
    if (!disposed && sequence === pageSequence)
      pageError.value = "作品列表更新失败；已有作品保留，请重试。";
  } finally {
    if (!disposed && sequence === pageSequence) loading.value = false;
  }
}
async function loadTimeline() {
  const sequence = ++timelineSequence;
  const hours = rangeHours.value;
  timelineController?.abort();
  timelineController = new AbortController();
  timelineLoading.value = true;
  timelineError.value = "";
  try {
    const result = await publicTimeline(hours, timelineController.signal);
    if (!disposed && sequence === timelineSequence) timeline.value = result;
  } catch {
    if (!disposed && sequence === timelineSequence)
      timelineError.value = "时间轴更新失败，当前不是最新数据。请重试。";
  } finally {
    if (!disposed && sequence === timelineSequence)
      timelineLoading.value = false;
  }
}
async function refreshAll() {
  await Promise.all([loadPage(), loadTimeline()]);
}
async function openLightbox(id: number) {
  const sequence = ++animationSequence;
  animationController?.abort();
  animationController = new AbortController();
  animationDocument.value = "";
  animationAvailable.value = false;
  animationError.value = "";
  animationLoading.value = true;
  animationPaused.value = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
  focusBeforeDialog = document.activeElement as HTMLElement;
  lightboxId.value = id;
  oldOverflow = document.body.style.overflow;
  document.body.style.overflow = "hidden";
  await nextTick();
  lightbox.value?.showModal();
  try {
    const result = await publicAnimation(id, animationController.signal);
    if (disposed || sequence !== animationSequence || lightboxId.value !== id) return;
    animationDocument.value = result.document;
    animationAvailable.value = result.animated;
  } catch {
    if (!disposed && sequence === animationSequence && lightboxId.value === id)
      animationError.value = "动画加载失败，暂时展示静态图片；关闭后可重新打开。";
  } finally {
    if (sequence === animationSequence) animationLoading.value = false;
  }
}
function closeLightbox() {
  lightbox.value?.close();
}
function onDialogClose() {
  animationSequence++;
  animationController?.abort();
  animationDocument.value = "";
  animationAvailable.value = false;
  animationLoading.value = false;
  animationError.value = "";
  document.body.style.overflow = oldOverflow;
  lightboxId.value = 0;
  focusBeforeDialog?.focus();
}
function onBackdropClick(event: MouseEvent) {
  if (!lightbox.value || event.target !== lightbox.value) return;
  const rect = lightbox.value.getBoundingClientRect();
  if (
    event.clientX < rect.left ||
    event.clientX > rect.right ||
    event.clientY < rect.top ||
    event.clientY > rect.bottom
  )
    closeLightbox();
}
function onVisible() {
  if (!document.hidden) void refreshAll();
}
watch(rangeHours, () => {
  timeline.value = null;
  void loadTimeline();
});
watch(currentPage, () => {
  void loadPage();
});
onMounted(() => {
  void refreshAll();
  timer = setInterval(() => {
    if (!document.hidden) void refreshAll();
  }, 60_000);
  document.addEventListener("visibilitychange", onVisible);
});
onBeforeUnmount(() => {
  disposed = true;
  clearInterval(timer);
  pageController?.abort();
  timelineController?.abort();
  animationSequence++;
  animationController?.abort();
  document.removeEventListener("visibilitychange", onVisible);
  if (lightboxId.value) document.body.style.overflow = oldOverflow;
});
</script>

<style scoped>
.animation-frame {
  display: block;
  width: 100%;
  height: min(68vh, 760px);
  min-height: 240px;
  border: 0;
  background: #f6f7f3;
  pointer-events: none;
}
.animation-caption {
  color: var(--muted);
  font-size: 12px;
  margin: 12px 0;
}
.animation-toggle {
  margin: 12px 0;
}
.artwork-explanation {
  color: var(--muted);
  font-size: 12px;
  line-height: 1.8;
  margin: 0 0 20px;
}
.observatory {
  --paper: #f6f7f4;
  --surface: #fff;
  --ink: #20352e;
  --muted: #68776f;
  --line: #dfe5df;
  --accent: #177554;
  min-height: 100vh;
  background: var(--paper);
  color: var(--ink);
  font-family: Inter, "PingFang SC", "Microsoft YaHei", sans-serif;
}
.shell {
  max-width: 1280px;
  margin: auto;
  padding: 0 40px;
}
.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  height: 96px;
  border-bottom: 1px solid var(--line);
}
.wordmark {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 16px;
  font-weight: 750;
  text-decoration: none;
  color: inherit;
}
.brand-mark {
  display: grid;
  place-items: center;
  width: 32px;
  height: 32px;
  border-radius: 9px;
  background: var(--ink);
  color: #fff;
  font-size: 24px;
}
.wordmark-en {
  font-size: 10px;
  letter-spacing: 1.8px;
  color: var(--muted);
  margin-left: 7px;
}
.topbar-right {
  display: flex;
  align-items: center;
  gap: 22px;
}
.sampling {
  font-size: 12px;
  color: var(--muted);
  display: flex;
  align-items: center;
  gap: 8px;
}
.sampling i {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--accent);
  box-shadow: 0 0 0 4px #17755412;
}
.button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  min-height: 40px;
  border: 1px solid var(--line);
  background: var(--surface);
  color: var(--ink);
  border-radius: 10px;
  padding: 9px 14px;
  font-size: 12px;
  font-weight: 600;
  transition:
    border-color 0.2s,
    background 0.2s;
}
.button:hover:not(:disabled) {
  border-color: var(--accent);
  background: #17755408;
}
.button:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
button:focus-visible,
a:focus-visible {
  outline: 3px solid #68ad91;
  outline-offset: 4px;
}
.intro {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 36px;
  padding: 55px 0 38px;
}
.eyebrow {
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 2px;
  color: var(--accent);
  margin: 0 0 15px;
}
h1 {
  font-size: 42px;
  line-height: 1.3;
  letter-spacing: -1.7px;
  font-weight: 750;
  margin: 0;
}
h1 span {
  color: var(--accent);
}
.intro-copy {
  font-size: 13px;
  line-height: 1.9;
  color: var(--muted);
  margin: 18px 0 0;
  max-width: 620px;
}
.mobile-break {
  display: none;
}
.intro-note {
  padding: 12px 0 12px 25px;
  border-left: 1px solid var(--line);
  flex-shrink: 0;
}
.note-number {
  font-family: monospace;
  font-size: 11px;
  letter-spacing: 2px;
  color: var(--muted);
}
.intro-note p {
  font-size: 14px;
  margin: 10px 0;
}
.intro-note p span {
  color: #9bb2a4;
  margin: 0 9px;
}
.intro-note small {
  font-size: 11px;
  color: var(--muted);
}
.metrics {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  margin-bottom: 28px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 16px;
  padding: 25px 0;
}
.metric {
  padding: 0 28px;
}
.metric + .metric {
  border-left: 1px solid var(--line);
}
.metric > span {
  font-size: 12px;
  color: var(--muted);
}
.metric strong {
  display: block;
  font-size: 38px;
  font-weight: 600;
  letter-spacing: -1.5px;
  line-height: 1.4;
  margin-top: 7px;
  font-variant-numeric: tabular-nums;
}
.metric strong small {
  font-size: 14px;
  color: var(--muted);
  font-weight: 400;
  margin-left: 5px;
  letter-spacing: 0;
}
.metric p {
  font-size: 11px;
  color: var(--muted);
  margin: 6px 0 0;
  line-height: 1.7;
}
.text-alert {
  color: #b65148;
}
.chart-panel {
  border: 1px solid var(--line);
  border-radius: 16px;
  background: var(--surface);
  padding: 27px 28px;
}
.gallery {
  margin-top: 49px;
}
.section-heading {
  display: flex;
  justify-content: space-between;
  align-items: flex-end;
  gap: 24px;
  margin-bottom: 22px;
}
.section-heading .eyebrow {
  margin-bottom: 9px;
}
.section-heading h2 {
  font-size: 22px;
  font-weight: 650;
  letter-spacing: -0.5px;
  margin: 0;
  display: flex;
  align-items: center;
  gap: 12px;
}
.count {
  font: 11px monospace;
  color: var(--muted);
  border: 1px solid var(--line);
  border-radius: 6px;
  padding: 3px 7px;
}
.section-heading p:not(.eyebrow) {
  font-size: 12px;
  color: var(--muted);
  margin: 10px 0 0;
}
.archive-note {
  font-size: 11px;
  color: var(--muted);
  white-space: nowrap;
}
.art-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 24px;
}
.art-card {
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 13px;
  overflow: hidden;
  transition:
    transform 0.2s,
    box-shadow 0.2s;
}
.art-card:hover {
  transform: translateY(-3px);
  box-shadow: 0 10px 26px #20352e0a;
}
.art-frame {
  position: relative;
  display: block;
  width: 100%;
  aspect-ratio: 4/3;
  padding: 24px;
  background: #edf0e9;
  border: 0;
  overflow: hidden;
  cursor: zoom-in;
}
.art-card:nth-child(3n + 2) .art-frame {
  background: #f0ede7;
}
.art-card:nth-child(3n) .art-frame {
  background: #eaf0ef;
}
.art-frame img {
  width: 100%;
  height: 100%;
  object-fit: contain;
  filter: drop-shadow(0 5px 6px #20352e08);
}
.art-index {
  position: absolute;
  left: 14px;
  top: 12px;
  font: 10px monospace;
  color: #718276;
  z-index: 1;
}
.expand-mark {
  position: absolute;
  bottom: 12px;
  right: 12px;
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  background: #ffffffd9;
  border-radius: 50%;
  color: #20352e;
  transition: transform 0.2s;
}
.art-frame:hover .expand-mark {
  transform: rotate(8deg);
}
.art-meta {
  padding: 17px 18px 12px;
  display: flex;
  align-items: start;
  justify-content: space-between;
  gap: 12px;
}
.art-meta > div {
  min-width: 0;
}
.art-meta h3 {
  font-size: 14px;
  font-weight: 650;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  margin: 0;
}
.art-meta p {
  font: 11px monospace;
  color: var(--muted);
  margin: 6px 0 0;
}
.art-meta p span {
  margin: 0 5px;
}
.file-badge {
  font: 9px monospace;
  letter-spacing: 1px;
  border: 1px solid var(--line);
  padding: 3px 5px;
  border-radius: 4px;
  color: var(--muted);
}
.art-footer {
  display: flex;
  justify-content: space-between;
  margin: 0 18px;
  padding: 12px 0 15px;
  border-top: 1px solid var(--line);
  font: 10px monospace;
  color: var(--muted);
}
.pagination {
  display: flex;
  justify-content: center;
  align-items: center;
  gap: 25px;
  margin-top: 30px;
}
.pagination > span {
  font: 12px monospace;
  color: var(--muted);
}
.footer {
  display: flex;
  justify-content: space-between;
  gap: 20px;
  border-top: 1px solid var(--line);
  margin-top: 52px;
  padding: 25px 0 32px;
  font-size: 10px;
  color: var(--muted);
  letter-spacing: 0.4px;
}
.footer b {
  margin: 0 10px;
}
.notice {
  padding: 14px 17px;
  border-radius: 9px;
  font-size: 12px;
  margin-bottom: 18px;
  line-height: 1.8;
}
.error {
  background: #b651480b;
  border: 1px solid #b6514833;
  color: #a14038;
}
.notice button {
  font-weight: 700;
  text-decoration: underline;
  margin-left: 12px;
}
.empty-state {
  text-align: center;
  padding: 65px 20px;
  border: 1px dashed var(--line);
  border-radius: 14px;
  color: var(--muted);
}
.empty-state > span {
  font-size: 36px;
  color: var(--accent);
}
.empty-state h3 {
  font-size: 16px;
  margin: 15px 0 10px;
  color: var(--ink);
}
.empty-state p {
  font-size: 13px;
  line-height: 1.8;
}
.image-fallback {
  display: flex;
  flex-direction: column;
  gap: 10px;
  font-size: 13px;
  color: #68776f;
}
.image-fallback span {
  font-size: 11px;
}
.skeleton {
  aspect-ratio: 1;
  background: linear-gradient(110deg, #e7ebe4 30%, #f0f2ee 45%, #e7ebe4 60%);
  background-size: 200% 100%;
  border-radius: 13px;
  animation: shimmer 1.6s infinite;
}
.lightbox {
  background: var(--surface);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: 18px;
  padding: 22px;
  width: min(960px, 92vw);
  max-height: 90vh;
  overflow: auto;
}
.lightbox::backdrop {
  background: #14241dc9;
  backdrop-filter: blur(7px);
}
.lightbox-top {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 18px;
}
.lightbox h2 {
  font-size: 15px;
}
.lightbox h2 span {
  font: 12px monospace;
  color: var(--muted);
  margin-left: 10px;
}
.lightbox > img {
  display: block;
  max-width: 100%;
  max-height: 68vh;
  margin: auto;
  object-fit: contain;
}
.original-link {
  display: block;
  text-align: center;
  margin-top: 17px;
  font-size: 12px;
  color: var(--accent);
}
@keyframes shimmer {
  to {
    background-position: -200% 0;
  }
}
:global(.dark .observatory) {
  --paper: #141e19;
  --surface: #1b2922;
  --ink: #e2ece5;
  --muted: #a0b2a6;
  --line: #324339;
  --accent: #87cbb0;
}
:global(.dark .observatory .brand-mark) {
  background: #87cbb0;
  color: #14241d;
}
@media (max-width: 900px) {
  .shell {
    padding: 0 24px;
  }
  .intro-note {
    display: none;
  }
  h1 {
    font-size: 36px;
  }
  .art-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .archive-note {
    display: none;
  }
  .metric {
    padding: 0 20px;
  }
  .metric strong {
    font-size: 33px;
  }
  .wordmark-en {
    display: none;
  }
}
@media (max-width: 560px) {
  .shell {
    padding: 0 18px;
  }
  .topbar {
    height: 78px;
    gap: 10px;
  }
  .sampling {
    display: none;
  }
  .wordmark {
    font-size: 14px;
  }
  .intro {
    padding: 35px 0 27px;
  }
  .intro .eyebrow {
    font-size: 8px;
    letter-spacing: 1.5px;
  }
  .mobile-break {
    display: block;
  }
  h1 {
    font-size: 34px;
    line-height: 1.45;
    letter-spacing: -1px;
  }
  .intro-copy {
    font-size: 12px;
  }
  .metrics {
    padding: 18px 0;
    gap: 0;
    border-radius: 12px;
  }
  .metric {
    padding: 0 12px;
  }
  .metric > span {
    font-size: 10px;
    line-height: 1.5;
    display: block;
  }
  .metric strong {
    font-size: 28px;
  }
  .metric p {
    font-size: 10px;
  }
  .chart-panel {
    padding: 20px 16px;
  }
  .section-heading h2 {
    font-size: 20px;
  }
  .section-heading p:not(.eyebrow) {
    line-height: 1.7;
  }
  .gallery {
    margin-top: 34px;
  }
  .art-grid {
    grid-template-columns: 1fr;
    gap: 20px;
  }
  .footer {
    flex-direction: column;
    gap: 12px;
    margin-top: 35px;
  }
  .art-frame {
    aspect-ratio: 4/3;
  }
  .button {
    min-height: 44px;
  }
}
@media (prefers-reduced-motion: reduce) {
  *,
  *::before,
  *::after {
    animation: none !important;
    transition: none !important;
    transform: none !important;
  }
}
</style>
