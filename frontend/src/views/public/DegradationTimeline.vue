<template>
  <section class="timeline" aria-labelledby="reasoning-timeline-title" :aria-busy="loading">
    <header class="timeline-header">
      <div>
        <p class="timeline-eyebrow">REASONING STATUS</p>
        <h2 id="reasoning-timeline-title">推理表现时间轴</h2>
        <p class="timeline-description">每轮检测结束后随机抽取一个账号独立测试 · 不混入全量账号统计</p>
      </div>
      <div class="range-options" aria-label="时间范围">
        <button v-for="option in RANGES" :key="option.hours" type="button"
          :aria-pressed="option.hours === range" @click="emit('update:range', option.hours)">
          {{ option.label }}
        </button>
      </div>
    </header>

    <div class="status-summary">
      <strong>{{ rangeLabel }}检测时间线</strong><span>{{ slots.length }} 个时段</span>
    </div>
    <div v-if="!sampleMode" class="status-summary">
      <span class="latest-state" :class="`latest-${timeline?.current_state || 'unknown'}`">
        <i aria-hidden="true"></i>{{ loading ? '正在更新…' : stateText }}
      </span>
      <span class="window-ratio">窗口答对比例 <strong>{{ healthyPercent }}</strong></span>
    </div>

    <div v-if="slots.length" class="status-track" role="group" aria-label="推理状态时间条；左右方向键选择时段"
      :style="{ gridTemplateColumns: `repeat(${sampleMode ? 48 : slots.length}, minmax(0, 1fr))` }">
      <button v-for="(slot, index) in slots" :key="slot.start" type="button"
        class="status-slot" :class="[`slot-${slot.state}`, { selected: selectedIndex === index }]"
        :data-state="slot.state" :tabindex="selectedIndex === index ? 0 : -1"
        :aria-label="slot.description" :aria-pressed="selectedIndex === index"
        aria-describedby="timeline-detail" @focus="active = index" @mouseenter="active = index"
        @click="active = index" @keydown="navigate($event, index)">
        <span aria-hidden="true"></span>
      </button>
    </div>
    <div v-else class="track-placeholder">{{ loading ? '正在读取探测记录…' : '所选窗口暂无探测记录' }}</div>
    <div class="time-axis"><span>{{ rangeLabel }}前</span><span>现在</span></div>

    <div class="sample-legend" v-if="sampleMode">
      <span><i class="legend-healthy"></i>通过</span><span><i class="legend-degraded"></i>未通过</span><span><i class="legend-unknown"></i>请求失败</span><span><i class="legend-empty"></i>无数据</span><span><i class="legend-running"></i>检测中</span>
      <span class="round-count">{{ rangeLabel }}检测 {{ timeline?.total || 0 }} 轮</span>
    </div>
    <div v-if="sampleMode" class="latest-round" aria-live="polite">
      <span>最近一轮</span>
      <strong v-if="timeline?.latest_sample">{{ stateLabel(timeline.latest_sample.state) }} · {{ (timeline.latest_sample.duration_ms / 1000).toFixed(1) }} 秒 · {{ tokenLabel(timeline.latest_sample.output_tokens) }}</strong>
      <strong v-else>{{ timeline?.running ? '检测中，等待本轮结果' : '等待首轮抽测；历史全量结果不作抽样回填' }}</strong>
      <button v-if="timeline?.latest_sample" type="button" aria-label="查看最近一轮详情" @click="selectLatest">↗</button>
    </div>
    <div v-if="sampleMode" class="sample-schedule">
      <span>检测频率 <b>{{ timeline?.interval_minutes || 10 }} 分钟 / 轮</b></span>
      <span>最近检测 <b>{{ timeline?.last_probe_at ? stamp(Date.parse(timeline.last_probe_at)) : '—' }}</b></span>
      <span>下次检测 <b>{{ nextLabel }}</b></span>
    </div>
    <div id="timeline-detail" class="slot-detail" aria-live="polite" aria-atomic="true">
      <template v-if="selected">
        <div class="detail-heading"><span class="state-label" :class="`label-${selected.state}`">{{ selected.label }}</span><time>{{ selected.period }}</time></div>
        <p v-if="sampleMode && selected.sample">{{ stateLabel(selected.sample.state) }} · {{ (selected.sample.duration_ms / 1000).toFixed(1) }} 秒 · {{ tokenLabel(selected.sample.output_tokens) }} · {{ selected.sample.model }}</p>
        <p v-else-if="selected.total">共 {{ selected.total }} 次 · 答对 {{ selected.correct }} · 答错 {{ selected.degraded }} · 未判定 {{ selected.undetermined }}</p>
        <p v-else>此时段没有探测记录，不代表正常或异常。</p>
      </template>
      <p v-else>暂无数据不等于模型异常；探测完成后将自动更新。</p>
    </div>
    <label v-if="slots.length" class="touch-scrubber">滑动查看时段
      <input type="range" min="0" :max="slots.length - 1" :value="selectedIndex" step="1"
        :aria-valuetext="selected?.description" @input="active = Number(($event.target as HTMLInputElement).value)" />
    </label>
    <footer v-if="!sampleMode">
      <div class="status-legend" aria-label="时间条图例">
        <span><i class="legend-healthy"></i>全部答对</span>
        <span><i class="legend-degraded"></i>含答错</span>
        <span><i class="legend-unknown"></i>含未判定</span>
        <span><i class="legend-empty"></i>无数据</span>
      </div>
      <p class="timeline-note">同段优先显示答错，其次未判定；颜色不表示该段全部账号异常。答对比例按探测次数计算，并非服务在线率。首尾段仅统计窗口内记录。</p>
    </footer>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { DegradationTimeline } from "@/api/degradation";
const props = defineProps<{ timeline: DegradationTimeline | null; range: number; loading?: boolean }>();
const emit = defineEmits<{ (event: "update:range", value: number): void }>();
const RANGES = [{ hours: 24, label: "24 小时" }, { hours: 72, label: "3 天" }, { hours: 168, label: "7 天" }] as const;
const active = ref(-1);
const sampleMode = computed(() => props.timeline?.mode === 'round_sample');
function stateLabel(state: string) { return ({ healthy: '通过', degraded: '未通过', unknown: '请求失败', empty: '无数据', running: '检测中' } as Record<string,string>)[state] || '无数据'; }
function tokenLabel(tokens: number | null | undefined) { return tokens == null ? 'tokens 未返回' : `${tokens} tokens`; }
const nextLabel = computed(() => {
 if (props.timeline?.running) return '检测中';
 const at = Date.parse(props.timeline?.next_probe_at ?? '');
 if (!Number.isFinite(at) || at <= Date.parse(props.timeline?.generated_at ?? '')) return '等待本轮全量检测结束';
 return `约 ${stamp(at)}（轮后抽测）`;
});
function selectLatest() { const index = slots.value.findIndex(s => s.sample?.id === props.timeline?.latest_sample?.id); if (index >= 0) active.value = index; }

watch(() => props.timeline, () => { active.value = -1; });
const rangeLabel = computed(() => props.range === 24 ? '24 小时' : `${props.range / 24} 天`);
const healthyPercent = computed(() => props.timeline?.total ? `${(props.timeline.correct / props.timeline.total * 100).toFixed(1)}%` : '—');
const stateText = computed(() => {
  if (!props.timeline?.last_probe_at) return '等待探测数据';
  return `最近单次探测 · ${props.timeline.current_state === 'healthy' ? '答对' : props.timeline.current_state === 'degraded' ? '答错' : '未判定'}`;
});
const dateFormat = new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false });
function stamp(value: number) { return Number.isFinite(value) ? dateFormat.format(value) : '—'; }
const slots = computed(() => {
  const now = Date.parse(props.timeline?.generated_at ?? '');
  const start = now - props.range * 3600000;
  const span = (props.timeline?.bucket_minutes || 20) * 60000;
  return (props.timeline?.buckets ?? []).map(bucket => {
    const state = sampleMode.value ? (bucket.state || 'empty') : !bucket.total ? 'empty' : bucket.degraded > 0 ? 'degraded' : bucket.undetermined > 0 ? 'unknown' : 'healthy';
    const label = sampleMode.value ? stateLabel(state) : ({ healthy: '全部答对', degraded: '含答错', unknown: '含未判定', empty: '无数据' } as Record<string,string>)[state];
    const from = Date.parse(bucket.start);
    const period = `${stamp(Math.max(from, start))} — ${stamp(Math.min(from + span, now))}`;
    return { ...bucket, state, label, period, description: `${period} · ${label} · 共 ${bucket.total} 次（答对 ${bucket.correct} / 答错 ${bucket.degraded} / 未判定 ${bucket.undetermined}）` };
  });
});
const selectedIndex = computed(() => active.value >= 0 && active.value < slots.value.length ? active.value : slots.value.length - 1);
const selected = computed(() => slots.value[selectedIndex.value]);
function navigate(event: KeyboardEvent, index: number) {
  let target = index;
  if (event.key === 'ArrowLeft') target--;
  else if (event.key === 'ArrowRight') target++;
  else if (event.key === 'Home') target = 0;
  else if (event.key === 'End') target = slots.value.length - 1;
  else return;
  event.preventDefault();
  active.value = Math.min(Math.max(target, 0), slots.value.length - 1);
  const track = (event.currentTarget as HTMLElement).parentElement;
  (track?.children[active.value] as HTMLElement | undefined)?.focus();
}
</script>

<style scoped>
.timeline { color: var(--ink); --healthy: #2d8564; --degraded: #c9574e; --unknown: #bf8a32; --empty: #dce3dd; }
.timeline-header { display:flex; justify-content:space-between; align-items:center; gap:20px; flex-wrap:wrap; }
.timeline-eyebrow { color:var(--muted); font-size:10px; letter-spacing:.16em; margin:0 0 8px; }
h2 { font-size:18px; font-weight:650; letter-spacing:-.02em; margin:0; }
.timeline-description { color:var(--muted); font-size:12px; margin:8px 0 0; line-height:1.7; }
.range-options { display:flex; padding:4px; gap:3px; background:var(--surface); border:1px solid var(--line); border-radius:12px; }
.range-options button { padding:0 14px; min-height:40px; border-radius:8px; color:var(--muted); font-size:12px; }
.range-options button[aria-pressed=true] { background:#206449; color:#fff; }
.timeline button:focus-visible { outline:2px solid var(--accent); outline-offset:3px; }
.status-summary { display:flex; justify-content:space-between; align-items:center; flex-wrap:wrap; gap:12px; margin:26px 0 12px; font-size:12px; }
.latest-state { display:flex; align-items:center; gap:7px; color:var(--muted); }
.latest-state i { width:7px; height:7px; border-radius:50%; background:var(--unknown); }
.latest-healthy i { background:var(--healthy); }
.latest-degraded i { background:var(--degraded); }
.window-ratio { color:var(--muted); }
.window-ratio strong { color:var(--ink); font:600 18px ui-monospace,monospace; margin-left:8px; }
.status-track { display:grid; gap:4px; padding:4px 0; }
.status-slot { height:18px; padding:0; position:relative; border:0; background:none; cursor:pointer; }
.status-slot span { display:block; height:16px; width:100%; border-radius:3px; background:var(--empty); }
.slot-healthy span { background:var(--healthy); }
.slot-degraded span { background:var(--degraded); }
.slot-unknown span { background:var(--unknown); }
.slot-running span { background:#7b9acb; }
.status-slot.selected::after { content:''; display:block; position:absolute; width:100%; height:2px; bottom:0; background:var(--ink); border-radius:1px; }
.status-slot:hover span { filter:brightness(1.12); }
.track-placeholder { padding:22px; background:var(--surface); color:var(--muted); font-size:13px; text-align:center; }
.time-axis { display:flex; justify-content:space-between; margin-top:8px; color:var(--muted); font-size:11px; }
.slot-detail { margin-top:12px; padding:15px 18px; background:var(--surface); border:1px solid var(--line); border-radius:10px; min-height:86px; font-size:12px; }
.detail-heading { display:flex; align-items:center; flex-wrap:wrap; gap:8px 14px; }
.slot-detail time { font:11px ui-monospace,monospace; color:var(--muted); }
.slot-detail p { margin:9px 0 0; color:var(--muted); line-height:1.7; }
.state-label { font-weight:600; }
.label-healthy { color:var(--healthy); } .label-degraded { color:var(--degraded); } .label-unknown { color:var(--unknown); } .label-empty { color:var(--muted); }
.touch-scrubber { display:none; }
footer { margin-top:18px; }
.status-legend { display:flex; flex-wrap:wrap; gap:10px 20px; color:var(--muted); font-size:11px; }
.status-legend span { display:inline-flex; gap:6px; align-items:center; }
.status-legend i { width:8px; height:8px; border-radius:2px; background:var(--empty); }
.status-legend .legend-healthy { background:var(--healthy); } .status-legend .legend-degraded { background:var(--degraded); } .status-legend .legend-unknown { background:var(--unknown); }
.timeline-note { font-size:11px; line-height:1.8; color:var(--muted); margin:12px 0 0; }
:global(.dark) .timeline { --healthy:#63b990; --degraded:#e8897e; --unknown:#d5ad67; --empty:#35473c; }
.sample-legend { display:flex; flex-wrap:wrap; gap:10px; color:var(--muted); font-size:12px; margin-top:14px; }
.sample-legend span { display:flex; align-items:center; gap:4px; }
.sample-legend i { width:7px; height:7px; border-radius:1px; }
.legend-healthy { background:var(--healthy); } .legend-degraded { background:var(--degraded); } .legend-unknown { background:var(--unknown); } .legend-empty { background:var(--empty); } .legend-running { background:#7b9acb; }
.round-count { margin-left:auto; }
.latest-round { display:flex; align-items:center; gap:16px; margin-top:14px; padding:10px 16px; min-height:48px; border:1px solid var(--line); border-radius:4px; background:var(--surface); font-size:12px; }
.latest-round > span { color:var(--muted); flex-shrink:0; } .latest-round strong { font-weight:500; } .latest-round button { margin-left:auto; min-height:28px; min-width:28px; font-size:20px; }
.sample-schedule { display:flex; flex-wrap:wrap; justify-content:space-between; gap:12px; margin-top:16px; font-size:12px; color:var(--muted); }
.sample-schedule b { color:var(--ink); font-weight:400; margin-left:6px; }
@media (max-width:640px) {
  .timeline-header { gap:14px; }
  .range-options { width:100%; } .range-options button { flex:1; }
  .status-track { gap:3px; grid-template-columns:repeat(24,minmax(0,1fr)) !important; } .status-slot span { border-radius:2px; }
  .slot-detail { padding:12px; min-height:104px; }
  .touch-scrubber { display:flex; align-items:center; gap:12px; font-size:11px; color:var(--muted); margin-top:6px; }
  .touch-scrubber input { flex:1; min-width:0; height:44px; accent-color:var(--accent); }
}
</style>
