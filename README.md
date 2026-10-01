# 泄洪闸门操作协调服务

这是一个无外部依赖的 Go 后端，用本地内存仓库协调闸门登记、开度调整命令、安全联锁复核和执行结果。服务强调单闸门活动命令互斥、原子状态转换、执行令牌生命周期和稳定错误传播。

闸门可配置滑动开度调整窗口：在窗口时长内，按**已生效执行事实**累计开度行程（双向均按绝对调整量），累计加候选超过安全上限的新申请或 clear 复核会以 `adjustment_window_exceeded` 冲突被拒绝；被拒绝的请求不占用闸门、不签发令牌、不推进版本。事实滑出窗口后容量真实释放，历史在窗口投影中仍可查。

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

登记闸门示例：

```json
{
  "id": "gate-east",
  "name": "东侧泄洪闸",
  "max_aperture": 4.5,
  "initial_aperture": 0.5
}
```

可选地为闸门启用调整窗口（时长秒数与累计行程上限必须同时提供）：

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

`GET /gates/{id}/adjustment-window` 返回窗口配置、`used`/`remaining`、窗口内 `effective_facts` 以及已释放容量的 `released_facts`（含 completed 与 aborted outcome），用于解释合法边界、真实释放和已完成历史。

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

## 测试状态

测试有意延后。当前交付仅包含初始化基线和生产级 smoke 检查；后续任务阶段会添加自动化测试与红绿验证，不应把当前没有测试文件视为项目未完成。
