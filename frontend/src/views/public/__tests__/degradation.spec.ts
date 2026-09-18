import { mount, flushPromises } from "@vue/test-utils";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import Page from "../DegradationDetectionView.vue";
import Timeline from "../DegradationTimeline.vue";
import Card from "../../admin/DegradationDetectionCard.vue";
import {
  publicPage,
  publicTimeline,
  listGroups,
  listWorks,
  updateGroup,
  runNow,
  resetPublicStats,
  publicAnimation,
  DEFAULT_DEGRADATION_CONFIG,
} from "@/api/degradation";

vi.mock("@/api/degradation", async (original) => {
  const actual = await original<typeof import("@/api/degradation")>();
  return {
    ...actual,
    publicPage: vi.fn(),
    publicTimeline: vi.fn(),
    listGroups: vi.fn(),
    listWorks: vi.fn(),
    updateGroup: vi.fn(),
    runNow: vi.fn(),
    resetPublicStats: vi.fn(),
    publicAnimation: vi.fn(),
  };
});
const pageResult = {
  enabled: true,
  total: 25,
  page: 1,
  page_size: 12,
  items: [],
  headline: "鹈鹕骑行",
  model: "test",
  reasoning_effort: "low",
  interval_seconds: 600,
  last_status: "",
  last_finished_at: null,
};
const timelineResult = {
  range_hours: 24,
  bucket_minutes: 60,
  generated_at: "2026-09-16T00:30:00Z",
  total: 100,
  correct: 99,
  degraded: 1,
  undetermined: 0,
  healthy_ratio: 0.99,
  current_state: "healthy",
  suspended_accounts: 0,
  last_probe_at: "2026-09-16T00:00:00Z",
  buckets: [
    {
      start: "2026-09-16T00:00:00Z",
      total: 100,
      correct: 99,
      degraded: 1,
      undetermined: 0,
    },
  ],
};
const mounted: ReturnType<typeof mount>[] = [];
function render(
  component: typeof Page | typeof Card | typeof Timeline,
  options = {},
) {
  const wrapper = mount(component as typeof Page, options);
  mounted.push(wrapper);
  return wrapper;
}
beforeEach(() => {
  vi.clearAllMocks();
  vi.spyOn(window, "matchMedia").mockImplementation((query: string) => ({
    matches: false, media: query, onchange: null,
    addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
  }));
  vi.mocked(publicPage).mockResolvedValue({ ...pageResult });
  vi.mocked(publicTimeline).mockResolvedValue({ ...timelineResult });
  vi.mocked(listGroups).mockResolvedValue([
    {
      group_id: 42,
      group_name: "Test",
      platform: "openai",
      account_count: 1,
      config: {
        ...DEFAULT_DEGRADATION_CONFIG,
        enabled: true,
        expected_answer: "29",
      },
    },
  ]);
  vi.mocked(listWorks).mockResolvedValue({
    total: 0,
    items: [],
    page: 1,
    page_size: 24,
  });
  vi.mocked(updateGroup).mockImplementation(async (id, config) => ({
    group_id: id,
    config: { ...config },
  }));
  vi.mocked(runNow).mockResolvedValue({ queued: 1 });
  vi.mocked(resetPublicStats).mockResolvedValue({ reset_at: "2026-09-16T16:00:00Z" });
  vi.mocked(publicAnimation).mockResolvedValue({ document: "<html><body>animation</body></html>", animated: true });
  HTMLDialogElement.prototype.showModal = function () { this.setAttribute("open", ""); };
  HTMLDialogElement.prototype.close = function () { this.removeAttribute("open"); this.dispatchEvent(new Event("close")); };
});
afterEach(() => {
  mounted.splice(0).forEach((w) => w.unmount());
  vi.restoreAllMocks();
});

describe("public degradation page", () => {
  it("surfaces a timeline failure even when artworks load successfully", async () => {
    vi.mocked(publicTimeline).mockRejectedValue(new Error("HTTP 500"));
    const wrapper = render(Page);
    await flushPromises();
    expect(wrapper.text()).toContain("时间轴更新失败");
    expect(wrapper.text()).toContain("25");
    expect(wrapper.find('[role="alert"]').exists()).toBe(true);
  });
  it("ignores an older response after the range changes", async () => {
    let resolve72!: (value: typeof timelineResult) => void;
    let resolve168!: (value: typeof timelineResult) => void;
    vi.mocked(publicTimeline).mockImplementation(async (hours) => {
      if (hours === 72)
        return new Promise((resolve) => {
          resolve72 = resolve;
        });
      if (hours === 168)
        return new Promise((resolve) => {
          resolve168 = resolve;
        });
      return timelineResult;
    });
    const wrapper = render(Page);
    await flushPromises();
    wrapper.findComponent(Timeline).vm.$emit("update:range", 72);
    await flushPromises();
    wrapper.findComponent(Timeline).vm.$emit("update:range", 168);
    await flushPromises();
    resolve168({ ...timelineResult, range_hours: 168 });
    await flushPromises();
    resolve72({ ...timelineResult, range_hours: 72 });
    await flushPromises();
    expect(wrapper.findComponent(Timeline).props("timeline").range_hours).toBe(
      168,
    );
    expect(wrapper.findComponent(Timeline).props("range")).toBe(168);
  });
  it("paginates older artworks rather than always requesting page one", async () => {
    const wrapper = render(Page);
    await flushPromises();
    const next = wrapper
      .findAll("button")
      .find((b) => b.text().includes("下一页"))!;
    await next.trigger("click");
    await flushPromises();
    expect(publicPage).toHaveBeenLastCalledWith(2, 12, expect.any(AbortSignal));
  });
  it("distinguishes disabled publishing from a queued generation", async () => {
    vi.mocked(publicPage).mockResolvedValue({
      ...pageResult,
      total: 0,
      enabled: false,
    });
    const wrapper = render(Page);
    await flushPromises();
    expect(wrapper.text()).toContain("作品档案暂未开放");
    expect(wrapper.text()).not.toContain("正在排队");
  });
  it("uses a full-height status segment and discloses a mixed bucket", () => {
    const wrapper = render(Timeline, {
      props: { range: 24, timeline: timelineResult },
    });
    const slot = wrapper.find(".status-slot");
    expect(slot.attributes("data-state")).toBe("degraded");
    expect(slot.attributes("aria-label")).toContain("答对 99");
    expect(wrapper.find("svg").exists()).toBe(false);
    expect(wrapper.text()).toContain("含答错");
    expect(wrapper.text()).toContain("并非服务在线率");
  });
  it("distinguishes all-correct, incorrect, unresolved, and missing samples", () => {
    const wrapper = render(Timeline, {
      props: { range: 24, timeline: { ...timelineResult, buckets: [
        { start: "2026-09-15T20:00:00Z", total: 3, correct: 3, degraded: 0, undetermined: 0 },
        { start: "2026-09-15T21:00:00Z", total: 3, correct: 2, degraded: 1, undetermined: 0 },
        { start: "2026-09-15T22:00:00Z", total: 3, correct: 2, degraded: 0, undetermined: 1 },
        { start: "2026-09-15T23:00:00Z", total: 0, correct: 0, degraded: 0, undetermined: 0 },
      ] } },
    });
    expect(wrapper.findAll(".status-slot").map(b => b.attributes("data-state"))).toEqual(["healthy", "degraded", "unknown", "empty"]);
    expect(wrapper.find("#timeline-detail").text()).toContain("没有探测记录");
  });
  it("supports arrow, Home/End and mobile slider selection", async () => {
    const wrapper = render(Timeline, {
      props: { range: 24, timeline: { ...timelineResult, buckets: [
        { ...timelineResult.buckets[0], start: "2026-09-15T23:00:00Z" },
        { ...timelineResult.buckets[0] },
      ] } },
    });
    const slots = wrapper.findAll(".status-slot");
    await slots[1].trigger("keydown", { key: "ArrowLeft" });
    expect(slots[0].attributes("tabindex")).toBe("0");
    await slots[0].trigger("keydown", { key: "End" });
    expect(slots[1].attributes("tabindex")).toBe("0");
    await wrapper.find('input[type="range"]').setValue("0");
    expect(slots[0].attributes("aria-pressed")).toBe("true");
  });
  it("uses 73 equal status slots even when counts vary greatly", () => {
    const wrapper = render(Timeline, {
      props: { range: 24, timeline: { ...timelineResult, bucket_minutes: 20,
        buckets: Array.from({ length: 73 }, (_, i) => ({
          start: new Date(Date.parse(timelineResult.generated_at) - (72 - i) * 1200000).toISOString(),
          total: i, correct: i, degraded: 0, undetermined: 0,
        })),
      } },
    });
    expect(wrapper.findAll(".status-slot")).toHaveLength(73);
    expect(wrapper.find(".status-track").attributes("style")).toContain("repeat(73");
    expect(wrapper.find(".status-slot").attributes("style")).toBeUndefined();
  });
  it("describes static thumbnails and click-to-play previews", async () => {
    const wrapper = render(Page);
    await flushPromises();
    expect(wrapper.text()).toContain("鹈鹕骑自行车的 2D 动画（HTML + SVG）");
    expect(wrapper.text()).toContain("缩略图保持静止，点击查看后播放动画");
    expect(wrapper.text()).not.toContain("作品提示词包含糖果题");
  });
  it("loads animation only on click and destroys it on pause and close", async () => {
    vi.mocked(publicPage).mockResolvedValue({ ...pageResult, items: [
      { id: 346, account_id: 1, status: "completed", model: "test", reasoning_effort: "low", has_image: true, duration_ms: 1000, created_at: "", finished_at: null },
    ] });
    const wrapper = render(Page);
    await flushPromises();
    expect(publicAnimation).not.toHaveBeenCalled();
    expect(wrapper.find("iframe").exists()).toBe(false);
    expect(wrapper.find(".art-frame img").attributes("src")).toContain("/346/image");
    await wrapper.find(".art-frame").trigger("click");
    await flushPromises();
    expect(publicAnimation).toHaveBeenCalledWith(346, expect.any(AbortSignal));
    expect(wrapper.find("iframe").attributes("sandbox")).toBe("");
    expect(wrapper.find("iframe").attributes("srcdoc")).toContain("animation");
    await wrapper.find(".animation-toggle").trigger("click");
    expect(wrapper.find("iframe").exists()).toBe(false);
    await wrapper.find(".animation-toggle").trigger("click");
    expect(wrapper.find("iframe").exists()).toBe(true);
    await wrapper.find(".lightbox-top button").trigger("click");
    await flushPromises();
    expect(wrapper.find("iframe").exists()).toBe(false);
  });
  it("ignores animation responses received after closing the dialog", async () => {
    let resolve!: (value: { document: string; animated: boolean }) => void;
    vi.mocked(publicAnimation).mockImplementation(() => new Promise(r => { resolve = r; }));
    vi.mocked(publicPage).mockResolvedValue({ ...pageResult, items: [
      { id: 346, account_id: 1, status: "completed", model: "test", reasoning_effort: "low", has_image: true, duration_ms: 1000, created_at: "", finished_at: null },
    ] });
    const wrapper = render(Page);
    await flushPromises();
    await wrapper.find(".art-frame").trigger("click");
    await flushPromises();
    await wrapper.find(".lightbox-top button").trigger("click");
    resolve({ document: "<html>late</html>", animated: true });
    await flushPromises();
    expect(wrapper.find("iframe").exists()).toBe(false);
  });
  it.each(["static", "failed"])("keeps the image when animation is %s", async (mode) => {
    if (mode === "static") vi.mocked(publicAnimation).mockResolvedValue({ document: "<html></html>", animated: false });
    else vi.mocked(publicAnimation).mockRejectedValue(new Error("HTTP 404"));
    vi.mocked(publicPage).mockResolvedValue({ ...pageResult, items: [
      { id: 346, account_id: 1, status: "completed", model: "test", reasoning_effort: "low", has_image: true, duration_ms: 1000, created_at: "", finished_at: null },
    ] });
    const wrapper = render(Page);
    await flushPromises();
    await wrapper.find(".art-frame").trigger("click");
    await flushPromises();
    expect(wrapper.find("iframe").exists()).toBe(false);
    expect(wrapper.find("dialog img").exists()).toBe(true);
    expect(wrapper.text()).toContain(mode === "static" ? "没有可播放" : "动画加载失败");
  });
});

describe("group answer setting", () => {
  it("requires confirmation before a global reset and allows cancelling", async () => {
    const wrapper = render(Card, { props: { groupId: 42 } });
    await flushPromises();
    const open = () => wrapper.findAll("button").find(b => b.text() === "重置公开页统计")!;
    await open().trigger("click");
    expect(resetPublicStats).not.toHaveBeenCalled();
    await wrapper.findAll("button").find(b => b.text() === "取消")!.trigger("click");
    expect(resetPublicStats).not.toHaveBeenCalled();
    await open().trigger("click");
    await wrapper.findAll("button").find(b => b.text() === "确认重置全站统计")!.trigger("click");
    await flushPromises();
    expect(resetPublicStats).toHaveBeenCalledTimes(1);
    expect(updateGroup).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("已重置全站公开统计");
  });
  it("shows reset failures without falsely reporting success", async () => {
    vi.mocked(resetPublicStats).mockRejectedValue(new Error("重置请求失败"));
    const wrapper = render(Card, { props: { groupId: 42 } });
    await flushPromises();
    await wrapper.findAll("button").find(b => b.text() === "重置公开页统计")!.trigger("click");
    await wrapper.findAll("button").find(b => b.text() === "确认重置全站统计")!.trigger("click");
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toContain("重置请求失败");
    expect(wrapper.text()).not.toContain("已重置全站公开统计");
  });
  it("shows the reset boundary and empty public statistics", async () => {
    vi.mocked(publicTimeline).mockResolvedValue({
      ...timelineResult, reset_at: "2026-09-16T00:20:00Z",
      total: 0, correct: 0, degraded: 0, undetermined: 0,
      healthy_ratio: 0, current_state: "unknown", last_probe_at: null, buckets: [],
    });
    const wrapper = render(Page);
    await flushPromises();
    expect(wrapper.text()).toContain("统计起点");
    expect(wrapper.text()).toContain("等待探测数据");
    expect(wrapper.text()).not.toContain("100.0");
  });
  it("exposes saving to the parent group dialog and persists the edited answer", async () => {
    const wrapper = render(Card, { props: { groupId: 42 } });
    await flushPromises();
    await wrapper.find('input[placeholder="21"]').setValue("37");
    const saved = await (
      wrapper.vm as unknown as { save(): Promise<boolean> }
    ).save();
    expect(saved).toBe(true);
    expect(updateGroup).toHaveBeenCalledWith(
      42,
      expect.objectContaining({ expected_answer: "37" }),
    );
  });
  it("does not overwrite saved settings when loading the card fails", async () => {
    vi.mocked(listGroups).mockRejectedValue(new Error("load failed"));
    const wrapper = render(Card, { props: { groupId: 42 } });
    await flushPromises();
    const saved = await (
      wrapper.vm as unknown as { save(): Promise<boolean> }
    ).save();
    expect(saved).toBe(false);
    expect(updateGroup).not.toHaveBeenCalled();
  });
  it("saves the visible answer before immediately probing the same group", async () => {
    const wrapper = render(Card, { props: { groupId: 42 } });
    await flushPromises();
    await wrapper.find('input[placeholder="21"]').setValue("31");
    const run = wrapper
      .findAll("button")
      .find((b) => b.text().includes("立即探测本组"))!;
    await run.trigger("click");
    await flushPromises();
    expect(updateGroup).toHaveBeenCalledWith(
      42,
      expect.objectContaining({ expected_answer: "31" }),
    );
    expect(runNow).toHaveBeenCalledWith(42);
    expect(vi.mocked(updateGroup).mock.invocationCallOrder[0]).toBeLessThan(
      vi.mocked(runNow).mock.invocationCallOrder[0],
    );
  });
  it("does not enqueue when saving the new answer fails", async () => {
    vi.mocked(updateGroup).mockRejectedValue(new Error("save failed"));
    const wrapper = render(Card, { props: { groupId: 42 } });
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text().includes("立即探测本组"))!
      .trigger("click");
    await flushPromises();
    expect(runNow).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("save failed");
  });
});


describe("move degraded accounts to a group", () => {
  async function card(extra = {}) {
    vi.mocked(listGroups).mockResolvedValue([
      { group_id: 42, group_name: "Source", platform: "openai", account_count: 2, config: { ...DEFAULT_DEGRADATION_CONFIG, enabled: true, ...extra } },
      { group_id: 8, group_name: "Degraded pool", platform: "openai", account_count: 0, config: { ...DEFAULT_DEGRADATION_CONFIG } },
    ]);
    const wrapper = render(Card, { props: { groupId: 42 } });
    await flushPromises(); return wrapper;
  }
  it("defaults off and retains the original cooldown control", async () => {
    const w = await card();
    expect(w.get('[data-testid="degradation-move-switch"]').attributes('aria-checked')).toBe('false');
    expect(w.find('[data-testid="degradation-move-target"]').exists()).toBe(false);
    expect(w.text()).toContain('降智后暂停调度（分钟）');
  });
  it("enables move-only mode, excludes the source, and saves a numeric destination", async () => {
    const w = await card();
    await w.get('[data-testid="degradation-move-switch"]').trigger('click');
    expect(w.text()).not.toContain('降智后暂停调度（分钟）');
    expect(w.text()).toContain('不再添加降智冷却');
    expect(w.find('[data-testid="degradation-move-target"] option[value="42"]').exists()).toBe(false);
    await w.get('[data-testid="degradation-move-target"]').setValue('8');
    expect(await (w.vm as unknown as { save(): Promise<boolean> }).save()).toBe(true);
    expect(updateGroup).toHaveBeenLastCalledWith(42, expect.objectContaining({ move_on_degraded: true, move_target_group_id: 8 }));
  });
  it("rejects an enabled move without a destination", async () => {
    const w = await card();
    await w.get('[data-testid="degradation-move-switch"]').trigger('click');
    expect(await (w.vm as unknown as { save(): Promise<boolean> }).save()).toBe(false);
    expect(updateGroup).not.toHaveBeenCalled();
    expect(w.text()).toContain('请选择有效的目标分组');
  });
  it("restores saved settings and allows disabling move without losing the target", async () => {
    const w = await card({ move_on_degraded: true, move_target_group_id: 8 });
    expect((w.get('[data-testid="degradation-move-target"]').element as HTMLSelectElement).value).toBe('8');
    await w.get('[data-testid="degradation-move-switch"]').trigger('click');
    expect(w.text()).toContain('降智后暂停调度（分钟）');
    expect(await (w.vm as unknown as { save(): Promise<boolean> }).save()).toBe(true);
    expect(updateGroup).toHaveBeenLastCalledWith(42, expect.objectContaining({ move_on_degraded: false, move_target_group_id: 8 }));
  });
  it("rejects a deleted destination rather than silently switching it", async () => {
    const w = await card({ move_on_degraded: true, move_target_group_id: 999 });
    expect(await (w.vm as unknown as { save(): Promise<boolean> }).save()).toBe(false);
    expect(updateGroup).not.toHaveBeenCalled();
  });
});

 it("uses candy probe defaults without changing artwork settings", () => {
 expect(DEFAULT_DEGRADATION_CONFIG.model).toBe("gpt-6-astra");
 expect(DEFAULT_DEGRADATION_CONFIG.reasoning_effort).toBe("medium");
 expect(DEFAULT_DEGRADATION_CONFIG.expected_answer).toBe("21");
 expect(DEFAULT_DEGRADATION_CONFIG.preview_model).toBe("gpt-6-astra");
 expect(DEFAULT_DEGRADATION_CONFIG.preview_reasoning_effort).toBe("low");
 });

describe("round sample timeline", () => {
 it("renders 144 sample slots and upstream duration/tokens without bulk totals", () => {
  const sample = { id: 9, state: 'healthy', status: 'completed', duration_ms: 8000, output_tokens: 166, model: 'test', created_at: '2026-09-16T00:00:00Z', finished_at: '2026-09-16T00:00:08Z' };
  const wrapper = render(Timeline, {props: {range:24,timeline:{...timelineResult,mode:'round_sample',interval_minutes:10,latest_sample:sample,total:1,buckets:Array.from({length:144},(_,i)=>({start:new Date(Date.parse('2026-09-15T00:30:00Z')+i*600000).toISOString(),total:i===143?1:0,correct:i===143?1:0,degraded:0,undetermined:0,state:i===143?'healthy':'empty',sample:i===143?sample:undefined}))}}});
  expect(wrapper.findAll('.status-slot')).toHaveLength(144);
  expect(wrapper.text()).toContain('通过 · 8.0 秒 · 166 tokens');
  expect(wrapper.text()).toContain('24 小时检测 1 轮');
  expect(wrapper.text()).toContain('请求失败');
  expect(wrapper.text()).not.toContain('窗口答对比例');
 });
 it("does not invent tokens or historical samples",()=>{
  const wrapper=render(Timeline,{props:{range:24,timeline:{...timelineResult,mode:'round_sample',total:0,latest_sample:null,buckets:[]}}});
  expect(wrapper.text()).toContain('等待首轮抽测');
  expect(wrapper.text()).not.toContain('166 tokens');
 });
});
