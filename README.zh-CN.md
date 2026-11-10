# caddy-forwarded-host

[English](README.md) | 简体中文

一个 Caddy 请求匹配器，按请求的有效主机路由：默认在请求来自可信代理时用 `X-Forwarded-Host`，否则用入站 `Host`；可选开启的 `trust_any_peer` 允许接受任意来源的这个头。

适用于 CDN 或边缘代理（Cloudflare Workers、负载均衡、另一个 Caddy）把许多公网域名转发到同一个源站域名，而需要按公网域名分流、又不采信客户端可伪造的值的场景。

## 与内置 `header` 匹配器的关系

`@app header X-Forwarded-Host app.example.com` 会无条件采信这个头。`X-Forwarded-Host` 是普通客户端可设置的请求头，任何能直连源站的人都能伪造。默认情况下本模块只在 Caddy 已经判定对端是可信代理时才使用它，复用 server 现有的 [`trusted_proxies`](https://caddyserver.com/docs/caddyfile/options#trusted-proxies) 配置，不引入第二套信任边界；下文提到的 `trust_any_peer` 例外必须显式开启。

## 安装

```bash
xcaddy build --with github.com/kuoruan/caddy-forwarded-host
```

## 前置条件

- 代理地址必须写进 server 的 `trusted_proxies`，该选项即默认的信任模型。没有它时，本模块的行为等同于内置 `host` 匹配器。这是有效配置。唯一的例外是 `trust_any_peer`，见[绕过对端检查](#绕过对端检查)。
- 请求仍然要先进入某个站点块，而站点块是按入站 `Host` 选择的，不是按 `X-Forwarded-Host`。因此站点地址必须覆盖客户端实际发送的主机（见[边界](#边界)）。

## 用法

没有站点地址的代码块都是片段，需要放进站点块中使用。

```caddyfile
{
	servers {
		trusted_proxies static 173.245.48.0/20 103.21.244.0/22
	}
}

origin.example.com {
	@app forwarded_host app.example.com
	handle @app {
		reverse_proxy 127.0.0.1:8081
	}

	handle {
		respond "Not Found" 404
	}
}
```

主机名列表与通配符的规则跟内置 `host` 匹配器一致：通配符必须占满整个标签，支持占位符，比较不区分大小写，请求携带的端口会被忽略。配置中的主机名不应包含端口。此类配置可以解析，但不会匹配，与内置匹配器相同。

```caddyfile
@app forwarded_host app.example.com app2.example.com
```

取值规则：取最后一个 `X-Forwarded-Host` 字段里最右的逗号分隔项（即距离本服务器最近的一跳写入的值）。若该项为空或不是合法的 `host[:port]`，请求即不匹配。它不会向左寻找其他值，也不会回退到入站 `Host`。

### 把命中的主机写给后端

除非配置另行指定，后端收到的仍是源站 `Host`。当命中的主机只能在运行期确定（`*.example.com` 或列表）时，引用本匹配器在成功匹配后发布的变量：

```caddyfile
@apps forwarded_host *.example.com

handle @apps {
	reverse_proxy 127.0.0.1:8081 {
		header_up Host {http.vars.forwarded_host}
	}
}
```

`{http.vars.forwarded_host}` 即本次实际用于匹配的 authority：对端可信（或开启 `trust_any_peer`）时是 `X-Forwarded-Host` 的值，否则是入站 `Host`，端口会保留。

- 只在本匹配器命中时才有值，因此只能在该路由中使用。在 catch-all 路由里引用，它是空的，或者仍是同一请求中更早一次成功匹配留下的值。空值交给 `header_up Host` 时，Go 会回退到上游地址，后端收到的是 `127.0.0.1:8081` 这样的上游地址。
- 同一条路由里后面的 `vars` 指令可以覆盖它；`vars` 匹配器不能在同一 matcher set 里读它（set 内匹配器的求值顺序不确定）。
- 用 `not` 包裹这个匹配器时，内部匹配成功仍然会发布变量。取反发生在本模块运行之后。
- 它是请求的回显，不是授权判断。

### 严格模式

默认情况下，没有可用 `X-Forwarded-Host` 的请求会回退到入站 `Host`，该行为适用于同一个站点块同时服务直连与代理流量的部署。加上 `require_header` 后，只接受「来自可信代理且携带有效 `X-Forwarded-Host`」的请求（开启 `trust_any_peer` 时仅保留对 `X-Forwarded-Host` 的条件）：

```caddyfile
@app forwarded_host app.example.com {
	require_header
}
```

主机名写在匹配器所在行；块内接受 `require_header` 与 `trust_any_peer`。

| 请求情况 | 默认 | `require_header` |
| --- | --- | --- |
| 对端不可信，无论头内容 | 按 `Host` 匹配 | 不匹配 |
| 对端可信，头缺失或全空白 | 按 `Host` 匹配 | 不匹配 |
| 对端可信，头有效 | 按该值匹配 | 按该值匹配 |
| 对端可信，头有效但未命中 | 不匹配（不回退 `Host`）| 不匹配 |
| 对端可信，头有内容但无效 | 不匹配 | 不匹配 |

上表假设 `trust_any_peer` 未开启；开启后「对端不可信」的行按可信处理（见[绕过对端检查](#绕过对端检查)）。

### 绕过对端检查

若源站只能经代理访问，代理地址可能无法写成网段，出口地址也可能变化。`trust_any_peer` 让本匹配器对任何对端都采信 `X-Forwarded-Host`：

```caddyfile
@app forwarded_host *.example.com {
	trust_any_peer
}
```

它只影响本匹配器。`client_ip`、访问日志和 `reverse_proxy` 仍使用 server 依据 `trusted_proxies` 算出的判定，该判定不会被改写。

该选项把路由决定交给任何能建立连接的客户端：任何客户端发送 `X-Forwarded-Host` 即可选中本匹配器允许的某条路由，authority 校验只能拒绝格式非法的值。仅当入口由客户端无法伪造的手段限制在代理上时才应开启，例如防火墙规则、socket 文件权限或带认证的 sidecar，并且代理必须覆写客户端自带的 `X-Forwarded-Host`，不得原样透传。仅监听在私网地址上并不充分，同网段的其他主机同样可以伪造该头。列出代理网段仍是推荐配置。

开启后对端不再参与判断，`require_header` 只保留「必须携带有效的 `X-Forwarded-Host`」这一条件：

| 开启 `trust_any_peer` 时 | 默认 | `require_header` |
| --- | --- | --- |
| 头缺失或全空白 | 按 `Host` 匹配 | 不匹配 |
| 头有效 | 按该值匹配 | 按该值匹配 |
| 头有效但未命中 | 不匹配（不回退 `Host`）| 不匹配 |
| 头有内容但无效 | 不匹配 | 不匹配 |

### JSON

```json
{
  "forwarded_host": {
    "hosts": ["app.example.com", "app2.example.com"]
  }
}
```

需要严格模式时加 `"require_header": true`；要接受任意来源的头，加 `"trust_any_peer": true`。

## 使用场景

### 多个公网域名，一个源站

代理把许多租户域名转发到同一个源站域名，租户名单只有运行期才知道，而给每个租户签证书不可行。

```caddyfile
origin.example.com {
	@tenants forwarded_host *.example.com

	handle @tenants {
		reverse_proxy 127.0.0.1:8081 {
			header_up Host {http.vars.forwarded_host}
		}
	}

	handle {
		respond "unknown tenant" 404
	}
}
```

该配置使用一个站点块和一张 `origin.example.com` 证书；匹配器根据可信代理写入的 `X-Forwarded-Host` 确定租户主机名，并通过 `Host` 传递给后端。

### 改写 `Host` 的 Cloudflare Worker

Worker 把 `Host` 覆写成源站域名，原域名只留在单个 `X-Forwarded-Host` 值里。把 Cloudflare 的网段写进 `trusted_proxies`（也可以用 `http.ip_sources.cloudflare` 提供，来自 [`github.com/WeidiDeng/caddy-cloudflare-ip`](https://github.com/WeidiDeng/caddy-cloudflare-ip)，代价是多一个构建依赖），然后按第一个示例匹配公网域名。

### 迁移：新旧域名并存

```caddyfile
@both forwarded_host app.example.com app.example.net
handle @both {
	reverse_proxy 127.0.0.1:8081 {
		header_up Host {http.vars.forwarded_host}
	}
}
```

两个域名路由到同一后端，各自保留自己的 `Host`；下线旧域名只需修改一个 token，无需复制路由。

### 按公网域名分到不同后端

```caddyfile
origin.example.com {
	@blog forwarded_host blog.example.com
	handle @blog {
		reverse_proxy 127.0.0.1:8081
	}

	@shop forwarded_host shop.example.com
	handle @shop {
		reverse_proxy 127.0.0.1:8082
	}

	handle {
		respond "unknown host" 404
	}
}
```

分流依据是通过信任检查的值，灰度、蓝绿或按应用分流只需替换 `handle` 块，无需改动证书和站点地址。

### 同一站点块里直连与代理共存

```caddyfile
# 直连访客照常工作：入站 Host 即公网域名。
@direct forwarded_host app.example.com
handle @direct {
	reverse_proxy 127.0.0.1:8081
}

# 仅接受代理流量：要求对端可信且 X-Forwarded-Host 有效。
@proxied forwarded_host app.example.com {
	require_header
}
handle @proxied {
	reverse_proxy 127.0.0.1:8081
}
```

仅对绝不允许被直连的域名启用 `require_header`。

### 非适用场景：多个域名、同一后端、无需分流

```caddyfile
a.example.com, b.example.com {
	reverse_proxy 127.0.0.1:8081
}
```

如果没有任何逻辑依赖实际使用的域名，站点地址列表即可满足。引入本模块会增加一条对 `trusted_proxies` 的隐性依赖，且无法让第三个域名支持直连，因为它无法改变站点选择。

## 边界

- 站点块选择与 TLS 证书仍按入站 `Host` 和 SNI 决定。本匹配器仅在已选定的站点块内部决定路由。若希望 `app.example.com` 也能被直连访问，站点地址必须覆盖它（`*.example.com` 或域名列表），通常意味着需要用 DNS-01 签发泛域名证书。
- 它不会改写 `r.Host`；要改变后端看到的值，用 `header_up Host {http.vars.forwarded_host}`（或字面量）。
- `trust_any_peer` 使本匹配器不再采用 `trusted_proxies` 的判定：能连到源站的客户端即可自行选择路由，authority 校验只能拒绝格式非法的值。
- 它不是授权机制。信任 Cloudflare 的网段并不代表信任 Worker 写入该头的值；若代理将客户端自带的 `X-Forwarded-Host` 原样透传，`require_header` 无法拦截。
- 只处理 `X-Forwarded-Host`。`X-Forwarded-For` 归 `trusted_proxies` / `client_ip`；`X-Forwarded-Proto` 与主机匹配无关，也不会还原 TLS/SNI。
- 主机名里的占位符只适用于静态来源（环境变量或配置文件）。请求派生的占位符，例如 `forwarded_host {http.request.header.X-Tenant-Host}`，会让请求把通配符注入匹配器；该用法不受支持，也未做加固。
- 带尾点的 FQDN（`app.example.com.`）能通过校验，但不命中 `app.example.com`，也不会回退 `Host`。
- 静态文件服务与共享缓存的键是 `Host` + URL，而不是 `X-Forwarded-Host`：如果响应会被多个主机共享，需要发 `Vary: X-Forwarded-Host` 或关闭缓存。

## 排查

「对端可信、`X-Forwarded-Host` 存在但无效」很难发现：匹配器不回退，请求表现为普通 404，模块本身不输出任何日志。缩小范围的步骤：

1. 从 `trusted_proxies` 内的地址带上头发一次请求（本地测试时把自己的地址临时加进去）：`curl -H 'Host: origin.example.com' -H 'X-Forwarded-Host: app.example.com' https://origin.example.com/`
2. 看 Caddy 的访问日志。请求头会记录在里面，每条日志还记录了处理该请求的站点块。
3. 与「去掉这个头」的同一请求对比：如果该请求命中，则问题出在头的值上（最右项为空、带尾点，或完整 URL 这类非 authority 值）。

## 兼容范围

构建基线为 Caddy v2.10.2（Go 1.25），实测版本为 v2.11.9。

用 Go 1.27+ 构建的 Caddy 2.10.x 无法加载任何配置，原因是上游 `encoding/json` 的变更（`json.RawMessage` 变成了别名）。在 Go 1.27+ 上请用 Caddy 2.11.x，或用 Go 1.25/1.26 构建 2.10.x。细节见 [CONTEXT.md](CONTEXT.md)。
