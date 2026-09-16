-- 241_degradation_detection.sql
--
-- 降智检测（degradation detection）与公开 SVG 展示页所需的结构。
--
-- 设计要点：
--   * 分组级开关与参数放在 groups 上（degradation_detection_enabled / _config），
--     与官方已有的分组字段同表，避免额外 JOIN。
--   * 账号暂停复用官方 temp_unschedulable_until / temp_unschedulable_reason
--     （调度器天然认这个字段），另外用三个 degradation_* 列记录"这次暂停是降智
--     检测造成的"，从而与管理员手动停用调度严格区分：手动停用不改这三列，
--     探测与自动重新启用也只在 degradation_suspended_until 非空时才动作。
--   * 两个测试类型（degradation_probe 糖果判定 / degradation_preview 鹈鹕 SVG）
--     复用既有 test_settings + account_tests 表，不新建表。
--
-- 全部语句幂等，可重复执行。

-- ---------------------------------------------------------------- groups
ALTER TABLE groups ADD COLUMN IF NOT EXISTS degradation_detection_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE groups ADD COLUMN IF NOT EXISTS degradation_detection_config JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE groups ADD COLUMN IF NOT EXISTS degradation_preview_enabled BOOLEAN NOT NULL DEFAULT FALSE;

-- ---------------------------------------------------------------- accounts
-- 降智检测专用暂停标记。与 temp_unschedulable_until 同步写入，但语义独立：
-- 只有这里非空且未过期时，探测判定为"未降智"才会自动恢复调度。
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS degradation_suspended_until TIMESTAMPTZ;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS degradation_suspended_at TIMESTAMPTZ;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS degradation_suspend_note TEXT NOT NULL DEFAULT '';

-- 到期恢复扫描：worker 每分钟按 degradation_suspended_until 取需要复检/恢复的账号。
CREATE INDEX IF NOT EXISTS accounts_degradation_suspended_until
    ON accounts (degradation_suspended_until) WHERE degradation_suspended_until IS NOT NULL;

-- 分组开关扫描：只索引打开检测的分组。
CREATE INDEX IF NOT EXISTS groups_degradation_detection_enabled
    ON groups (id) WHERE degradation_detection_enabled;

-- ------------------------------------------------- account_tests 排程索引
-- 调度器按 (account_id, test_type) 取"上一次完成时间"决定是否到点，需要该索引。
CREATE INDEX IF NOT EXISTS account_tests_account_type_id
    ON account_tests (account_id, test_type, id DESC);

-- 上面的索引只覆盖到 id；MAX(created_at) 还需要回表。按 created_at 再建一条，
-- 12 万账号池每分钟扫一遍时可以把这段子查询压成 index-only scan。
CREATE INDEX IF NOT EXISTS account_tests_account_type_created
    ON account_tests (account_id, test_type, created_at DESC);

-- ------------------------------------------------------- 默认测试设置
-- 提示词/评估器/标准答案作为可编辑的默认配置写进 test_settings；
-- 分组可以覆盖 model / reasoning_effort / interval / timeout。
-- 使用 ON CONFLICT DO NOTHING：绝不覆盖管理员已经改过的配置。
INSERT INTO test_settings (test_type, enabled, user_visible, config) VALUES
(
    'degradation_probe',
    FALSE,
    FALSE,
    jsonb_build_object(
        'prompt', '在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）  形状 | 苹果味 | 桃子味 | 西瓜味 圆形 | 7 | 9 | 8 五角星形 | 7 | 6 | 4  不许联网，自己计算。只回答最少取出的糖果总数，使用一个整数，不要解释。',
        'model', 'gpt-6-astra',
        'reasoning_effort', 'medium',
        'evaluator', 'exact_answer',
        'expected_answer', '21',
        'answer_type', 'number',
        'answer_format', 'free_text',
        'answer_unit_mode', 'none',
        'timeout_seconds', 300
    )
),
(
    'degradation_preview',
    FALSE,
    TRUE,
    jsonb_build_object(
        'prompt', E'做一张精细的SVG图片，内容是鹈鹕骑着自行车在孙悟空开的大型飞机机翼上骑行，\n鹈鹕展开翅膀，上面托着秦始皇的北极熊，秦始皇骑在熊上打螺丝。鹈鹕喊出\n“在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）\n苹果味 桃子味 西瓜味\n圆形 7 9 8\n五角星形 7 6 4”的结果数值  ',
        'model', 'gpt-6-astra',
        'reasoning_effort', 'low',
        'evaluator', 'svg_structure',
        'expected_answer', '',
        'answer_format', 'free_text',
        'answer_unit_mode', 'none',
        'timeout_seconds', 300
    )
)
ON CONFLICT (test_type) DO NOTHING;
