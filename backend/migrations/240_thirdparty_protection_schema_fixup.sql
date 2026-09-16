-- 240_thirdparty_protection_schema_fixup.sql
--
-- 补齐 239 号迁移漏掉的列。
--
-- 背景：第三方二开（防降智 / 智能测试）没有附带自己的 DDL，239 号迁移是按它的
-- ent schema 与 SQL 逐字段推导补出来的。把 239 应用后用它的全部 SQL 语句逐条
-- 解析校验（Parse/Prepare）后发现 account_tests 还缺执行租约列 lease_until：
--   intelligent_test_queue.go  领取/回收任务时按 lease_until 判断租约是否过期
--   intelligent_test_actions.go 重新入队时把 lease_until 置空
-- 缺列会让智能测试后台 worker 每分钟报 "column \"lease_until\" does not exist"。
--
-- 239 已经在美东独服应用并登记校验和，按迁移不可变约束这里只能新增文件补列。
-- 全部语句幂等，可重复执行。

-- ---------------------------------------------------------------- account_tests
ALTER TABLE account_tests ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;

-- 到期租约/在跑任务扫描用：后台 worker 每分钟按 (status, lease_until) 取任务。
CREATE INDEX IF NOT EXISTS account_tests_lease_until ON account_tests (lease_until);
