# 泄洪闸门操作协调服务

## 项目目标

该服务用于水工控制室协调泄洪闸门的命令申请、安全联锁复核和单次开度调整。系统在同一闸门上只允许一条已通过复核且尚未结束的执行命令，任何冲突都必须被显式拒绝，不能覆盖已经占用的闸门。

为控制连续动作对闸门结构的影响，闸门可额外配置开度调整窗口：在一个滑动时间窗口内，只按已生效执行事实累计开度行程（绝对调整量），累计值与候选调整量之和超过安全上限时，新申请和 clear 复核都必须被显式拒绝。窗口判据只承认已经生效的执行事实，待复核、已批准和被阻断的意图不占用窗口容量；事实滑出窗口后容量真实释放，历史仍然可查。

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

运行协调员通过 `POST /gates` 登记闸门。服务校验标识、名称、最大开度和初始开度，写入空闲状态；重复标识返回稳定冲突错误，不覆盖原记录。可同时提供 `adjustment_window_seconds` 与 `adjustment_window_limit` 启用开度调整窗口；两者必须同时给出，只给一个返回稳定的 invalid 错误。不配置窗口的闸门行为保持不变。

### request-gate-command

运行协调员通过 `POST /commands` 提交开度调整命令。服务读取闸门当前开度和限制，校验目标开度与申请信息，创建待复核命令并冻结起始开度。若闸门启用了调整窗口，服务在写锁内按已生效执行事实计算窗口内累计行程；累计值加上本次候选行程（当前开度到目标开度的绝对差）超过上限时，以稳定冲突 `adjustment_window_exceeded` 拒绝，命令不入库、不占用闸门。

### review-safety-interlock

安全复核员通过 `POST /commands/{id}/interlock` 提交复核结论。clear 结论会原子占用闸门并签发一次执行令牌；blocked 结论会把命令置为 rejected。其他命令先占用同一闸门时，当前 clear 结论返回冲突且不改变闸门。clear 复核在同一临界区内重新按当时的已生效事实进行窗口准入判定：窗口超额时返回 `adjustment_window_exceeded`，命令保持 pending、版本不递增、不签发令牌、闸门不被占用。窗口判定、闸门占用和令牌签发全部成功后才落库，任何一步失败都不写入。执行令牌只在全部准入成功后才生成，拒绝路径不会产生凭据。

### execute-gate-command

闸门操作员通过 `POST /commands/{id}/execute` 提交执行令牌。服务校验命令、令牌、闸门占用关系和目标开度，在临界区内完成 approved、executing、completed 或 aborted 的转换，并同步更新闸门状态。完成或中止（含模拟故障）都按移动前真实开度到最终开度登记一条已生效调整事实，事实与闸门、命令在同一临界区内提交。令牌在命令结束后失效，不能复用。

### get-adjustment-window

运行协调员通过 `GET /gates/{id}/adjustment-window` 读取闸门调整窗口投影：窗口配置、评估时刻、窗口内已用与剩余行程、窗口内有效事实列表，以及已滑出窗口、真实释放容量的事实列表。完成和中止的执行事实都保留且标注 outcome；未配置窗口的闸门返回 `enabled=false`。

## 状态与规则

- `gate` 状态：idle、reserved、moving、faulted。
- `gate_command` 状态：pending、approved、rejected、executing、completed、aborted。
- 只有 pending 命令可以复核。
- 只有 approved 命令可以执行，且执行令牌必须匹配。
- clear 复核与闸门占用必须同时成功；任一步失败时两者都不写入。
- blocked 复核使命令 rejected，不会占用闸门。
- 模拟故障会令命令 aborted、闸门 faulted，并保留最终开度。
- 已完成或已中止的命令不能再次执行。
- 调整窗口只按已生效执行事实（completed、aborted 结算）累计；意图状态不占用容量。
- 调整窗口的合法边界为等于上限；严格大于上限才拒绝。
- 被拒绝的申请或复核不占用闸门、不签发执行令牌、不递增命令版本。
- 窗口准入判定、闸门占用和事实登记共用仓库互斥锁；窗口计算与复核/执行并发时不存在超额放行窗口。
- 事实滑出滑动窗口后真实释放容量，但作为 released 历史持续可解释。

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

登记启用调整窗口的闸门示例：

```json
{
  "id": "gate-east",
  "name": "东侧泄洪闸",
  "max_aperture": 4.5,
  "initial_aperture": 0.5,
  "adjustment_window_seconds": 3600,
  "adjustment_window_limit": 1.0
}
```

窗口超额时返回统一冲突错误：

```json
{"code":"adjustment_window_exceeded","message":"aperture adjustment window exceeded: ..."}
```

错误响应统一为：

```json
{"code":"stable_error_code","message":"可读说明"}
```

## 验证计划

本初始化基线刻意不生成单元测试、夹具或浏览器测试文件，也不声明 `test_command`。后续任务阶段负责补充测试，并在该阶段建立红绿验证。当前每条工作流通过 `cmd/workflowcheck` 启动真实 HTTP 服务并调用公开接口，校验成功状态、持久化结果和关键失败分支。
