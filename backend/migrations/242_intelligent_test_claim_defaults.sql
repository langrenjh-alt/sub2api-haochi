-- 242_intelligent_test_claim_defaults.sql
--
-- 修复智能测试/降智检测任务永远领不到的问题。
--
-- 背景：account_tests.available_at 在 239 号迁移里是「可空、无默认值」，而任务
-- 领取语句是
--     WHERE q.status='queued' AND q.available_at<=NOW() AND ...
-- PostgreSQL 里 NULL <= NOW() 的结果是 NULL（不为真），于是任何没有显式写入
-- available_at 的排队任务都永远不会被 worker 取走。第三方二开自带的入队语句
-- （admin /intelligent-tests/run 与降智检测调度器）都不写这一列，所以合并进来的
-- 整套智能测试实际处于「只入队、不执行」的状态。
--
-- 修法：
--   1) 给该列一个符合语义的默认值 —— 新任务默认「立即可跑」；
--   2) 回填历史上排队的 NULL 行，让已经积压的任务跑起来。
-- 代码侧同时在领取语句里用 COALESCE(available_at, created_at) 兜底，双保险。
--
-- 全部语句幂等，可重复执行。

ALTER TABLE account_tests ALTER COLUMN available_at SET DEFAULT NOW();

-- 回填：仍处于排队状态、且从未写过 available_at 的任务视为立即可跑。
UPDATE account_tests SET available_at = NOW() WHERE available_at IS NULL AND status = 'queued';
