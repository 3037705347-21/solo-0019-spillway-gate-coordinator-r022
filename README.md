# 泄洪闸门操作协调服务

这是一个无外部依赖的 Go 后端，用本地内存仓库协调闸门登记、开度调整命令、安全联锁复核和执行结果。服务强调单闸门活动命令互斥、原子状态转换、执行令牌生命周期和稳定错误传播。

## 运行

```sh
go build ./...
go run ./cmd/server --addr 127.0.0.1:18119
```

健康入口为 `GET http://127.0.0.1:18119/health`。

## API

```text
POST /gates
GET  /gates/{id}
GET  /gates/{id}/adjustment-window
POST /commands
GET  /commands/{id}
POST /commands/{id}/interlock
POST /commands/{id}/execute
```

登记闸门示例（可选配置值班周期开度调整窗口：窗口秒数与累计调整上限必须同时提供，省略即不限制）：

```json
{
  "id": "gate-east",
  "name": "东侧泄洪闸",
  "max_aperture": 4.5,
  "initial_aperture": 0.5,
  "adjustment_window_seconds": 3600,
  "max_window_adjustment": 1.0
}
```

调整窗口只按**已生效执行事实**累计（完成或故障中止且闸门已 settle 的命令，按起始/最终开度的绝对调整量）。滑动窗口为半开区间 `(now-window, now]`，事实随时间自然滑出即释放额度。新申请与 clear 复核都会在仓库临界区内基于锁内时刻重算窗口；累计超过上限返回稳定冲突码 `adjustment_window_exceeded`，被拒绝请求不落库、不签发令牌、不占用闸门、不推进版本。`GET /gates/{id}/adjustment-window` 返回上限、已用、可用、窗口边界和构成已用量的事实明细。

提交命令示例：

```json
{
  "gate_id": "gate-east",
  "target_aperture": 2.25,
  "reason": "上游水位调整",
  "requested_by": "dispatcher-lin"
}
```

联锁复核示例：

```json
{
  "verdict": "clear",
  "verifier": "safety-wu",
  "note": "上下游条件满足"
}
```

执行示例：

```json
{
  "operator": "operator-chen",
  "token": "复核响应中的 execution_token",
  "simulate_fault": false
}
```

## 目录

- `cmd/server`：服务进程。
- `cmd/workflowcheck`：四条工作流的真实 HTTP 检查入口。
- `internal/bootstrap`：依赖装配。
- `internal/contracts`：公开数据契约。
- `internal/coordination`：业务工作流。
- `internal/domain`：实体、不变量和状态机。
- `internal/httpapi`：路由、解码和错误映射。
- `internal/storage`：互斥保护的内存仓库。

## 工作流检查

```sh
go run ./cmd/workflowcheck --workflow register-gate
go run ./cmd/workflowcheck --workflow request-gate-command
go run ./cmd/workflowcheck --workflow review-safety-interlock
go run ./cmd/workflowcheck --workflow execute-gate-command
go run ./cmd/workflowcheck --workflow adjustment-window
```

这些检查会创建临时 HTTP 服务并调用真实路由。它们不调用外部网络，也不依赖数据库。

## 测试

窗口域规则与仓库级原子性由 Go 单元测试覆盖，包含 `-race` 下的复核/执行并发场景：

```sh
go test -race ./...
```
