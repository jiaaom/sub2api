// Package trajectory 实现网关的 trajectory recording（完整交互录制）。
//
// 录制点（详见 openspec/changes/add-trajectory-recording/design.md）：
//   - A：客户端原始请求；D：回给客户端的响应。由 GinMiddleware / Around 在网关路由链最前端采集。
//   - B：改写后实际发往上游的请求；C：上游原始响应字节流。由 WrapHTTPUpstream 装饰 service.HTTPUpstream 采集。
//
// 磁盘是事实来源：exchange 先边流边写入 spool/<id>/，落定后整体 rename 到 raw/<date>/u<uid>/<id>/。
// 本包对上游代码的全部接线点登记在仓库根目录 AGENTS.md 的 Hook 清单中。
package trajectory
