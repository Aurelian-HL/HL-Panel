# 设备组协议健康链路待接入项

检查日期：2026-10-04。范围仅为本地 HL-panel 源码和回环测试；未访问、修改或压测 `zf.hongle.work`、`kl.hongle.cc`、`ht.hongle.work`。

## 已修复的单端点一致性问题

一条转发规则现在只能绑定一个 `SINGLE_SERVICE_ENDPOINT` 端点池。此前允许同一 `RuleID` 创建多个池，而协议健康发布只携带规则号和节点号，会把观察结果写入遍历到的任意池，造成不同设备组之间的健康状态串线。创建第二个绑定池现在返回冲突；客户仍只获得一条稳定入口。

## 尚未接入的 TCP 前置健康来源

新投影的端点候选 `LastHealthAt` 初始为空，`CandidateEligibleForNewConnection` 要求它必须是最近一次真实 TCP reachability 成功时间。当前源码没有把 TCP 探测结果写回端点候选的生产控制器，因此仅调用 VLESS Reality + Vision `ProtocolObserver` 不能让候选上线。

这不是通过 VLESS 探针写入 `LastHealthAt` 可以安全解决的问题：项目约束明确规定 TCP 探针只能否决已授权候选，协议探针不能伪造 TCP 心跳、续租 TCP 健康或解除 draining/offline 状态。需要后续实现独立、认证、带版本校验的 TCP reachability 写入接口/任务，并与网关成员刷新串接；在此之前，健康链路应保持 fail-closed。

## 当前验证

已通过：

```text
go test ./internal/control/memoryrepo -run 'TestBoundRuleHasOneStableEndpoint|TestBoundEndpointRejectsNetworkAndRuleChangesThatBreakBinding|TestEndpointHostnameVariantsShareOneAllocation' -count=1
go test ./internal/control/gatewaymembership ./internal/control/memoryrepo ./internal/control/forwarding/vlessconfig
```

没有部署，也没有把本地测试结果当成线上转发已验证。
