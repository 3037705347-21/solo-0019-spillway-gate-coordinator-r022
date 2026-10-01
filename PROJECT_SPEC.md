# 泄洪闸门操作协调服务

## 项目目标

该服务用于水工控制室协调泄洪闸门的命令申请、安全联锁复核和单次开度调整。系统在同一闸门上只允许一条已通过复核且尚未结束的执行命令，任何冲突都必须被显式拒绝，不能覆盖已经占用的闸门。

## 使用角色

- 运行协调员：登记闸门并提交开度调整命令。
- 安全复核员：提交联锁结论，决定命令是否通过。
- 闸门操作员：凭执行令牌启动命令并登记执行结果。

## 核心实体

- `gate`：闸门及其当前开度、运行状态、最大开度和活动命令。
- `gate_command`：一次开度调整申请，包含目标开度、申请原因、状态和执行令牌。
- `interlock_decision`：安全复核结论，状态为 clear 或 blocked。

## 业务流程

### register-gate

运行协调员通过 `POST /gates` 登记闸门。服务校验标识、名称、最大开度和初始开度，写入空闲状态；重复标识返回稳定冲突错误，不覆盖原记录。

### request-gate-command

运行协调员通过 `POST /commands` 提交开度调整命令。服务读取闸门当前开度和限制，校验目标开度与申请信息，创建待复核命令并冻结起始开度。

### review-safety-interlock

安全复核员通过 `POST /commands/{id}/interlock` 提交复核结论。clear 结论会原子占用闸门并签发一次执行令牌；blocked 结论会把命令置为 rejected。其他命令先占用同一闸门时，当前 clear 结论返回冲突且不改变闸门。

### adjustment-window

每个闸门可在登记时配置值班周期的开度调整窗口（窗口秒数与累计调整安全上限，必须成对配置，缺省表示不限制）。服务只统计已生效执行事实：命令完成或带故障中止都会记录最终开度、真实调整量与执行时刻；待复核、已批准未执行和被拒绝的申请不占用任何窗口额度。滑动窗口取半开区间 `(now-window, now]`，事实随时间自然滑出窗口即真实释放额度，不存在手工重置。新申请（`POST /commands`）和 clear 复核（`POST /commands/{id}/interlock`）都在仓库同一把锁内、基于锁内时刻重新计算窗口与拟调整量（按闸门实时开度到目标开度的绝对差），累计等于上限合法，超过则以稳定冲突码 `adjustment_window_exceeded` 拒绝。被拒绝时命令不落库或保持 pending、不签发令牌、不占用闸门、版本号不变；执行提交事实与复核并发时，排队进入临界区的复核必须看到刚提交的事实，不能因时钟竞态超额放行。`GET /gates/{id}/adjustment-window` 返回窗口是否启用、窗口边界、上限、已用、可用及构成已用量的事实明细，便于解释合法边界、释放与历史。

### execute-gate-command

闸门操作员通过 `POST /commands/{id}/execute` 提交执行令牌。服务校验命令、令牌、闸门占用关系和目标开度，在临界区内完成 approved、executing、completed 或 aborted 的转换，并同步更新闸门状态。令牌在命令结束后失效，不能复用。

## 状态与规则

- `gate` 状态：idle、reserved、moving、faulted。
- `gate_command` 状态：pending、approved、rejected、executing、completed、aborted。
- 只有 pending 命令可以复核。
- 只有 approved 命令可以执行，且执行令牌必须匹配。
- clear 复核与闸门占用必须同时成功；任一步失败时两者都不写入。
- blocked 复核使命令 rejected，不会占用闸门。
- 模拟故障会令命令 aborted、闸门 faulted，并保留最终开度。
- 已完成或已中止的命令不能再次执行。
- 调整窗口只按已生效执行事实（completed/aborted 且已 settle 的命令）累计，统计真实调整量绝对值；pending、approved 未执行和 rejected 不计入。
- 窗口内累计调整达到上限时，新申请和 clear 复核都返回 `adjustment_window_exceeded`；拒绝不占用、不签发、不推进版本。
- 事实因滑出窗口而释放额度；窗口计算与复核、执行共享同一临界区和锁内时刻，排队事务不能把已提交事实误判为未来事件。

## 模块边界

- `cmd/server`：进程入口、监听地址和退出控制。
- `internal/httpapi`：HTTP 路由、JSON 解码、状态码映射。
- `internal/coordination`：工作流编排、命令标识和执行令牌签发。
- `internal/domain`：实体、不变量和状态转换规则。
- `internal/storage`：带互斥保护的内存仓库和原子写入。
- `internal/contracts`：API 请求、响应和稳定错误结构。
- `internal/bootstrap`：运行时依赖装配。

## 接口

- `GET /health`
- `POST /gates`
- `GET /gates/{id}`
- `GET /gates/{id}/adjustment-window`
- `POST /commands`
- `GET /commands/{id}`
- `POST /commands/{id}/interlock`
- `POST /commands/{id}/execute`

错误响应统一为：

```json
{"code":"stable_error_code","message":"可读说明"}
```

## 验证计划

窗口域规则和仓库级原子性由 `internal/domain` 与 `internal/storage` 的 Go 单元测试覆盖（含 `-race` 下的复核/执行并发用例）。每条工作流通过 `cmd/workflowcheck` 启动真实 HTTP 服务并调用公开接口，校验成功状态、持久化结果和关键失败分支；`adjustment-window` 工作流校验新申请与复核两处的窗口拒绝、拒绝不落占用不签发不推进版本、事实明细可解释以及事实滑出后的真实释放。
