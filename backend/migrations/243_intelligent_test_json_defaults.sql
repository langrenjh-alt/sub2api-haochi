-- 243_intelligent_test_json_defaults.sql
--
-- 修复智能测试任务领取时 "unexpected end of JSON input" 的问题。
--
-- 背景：account_tests.evaluation / config_snapshot 是「可空、无默认值」的 jsonb
-- （239 号迁移按第三方 ent schema 推导时没有给默认值），而读取端直接
-- json.Unmarshal(空字节) —— 新入队的任务 evaluation 还是 NULL，于是每次领取都会
-- JSON 解析失败，队列永远排空不了。这与 242 修的 available_at 是同一类问题：
-- 第三方二开只带了代码、没带 DDL，补出来的 DDL 少了「非空默认值」这一层契约。
--
-- 修法：
--   1) 两列补上 '{}'::jsonb 默认值，新行不再为 NULL；
--   2) 回填历史 NULL 行；
--   3) 代码侧同时让扫描器容忍 NULL（双保险，兼容任何旧数据）。
--
-- 全部语句幂等，可重复执行。

ALTER TABLE account_tests ALTER COLUMN evaluation SET DEFAULT '{}'::jsonb;
ALTER TABLE account_tests ALTER COLUMN config_snapshot SET DEFAULT '{}'::jsonb;

UPDATE account_tests SET evaluation = '{}'::jsonb WHERE evaluation IS NULL;
UPDATE account_tests SET config_snapshot = '{}'::jsonb WHERE config_snapshot IS NULL;
