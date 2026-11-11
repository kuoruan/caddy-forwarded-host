# caddy-forwarded-host

[English](README.md) | 简体中文

Caddy 请求匹配器：在站点内按可信代理提供的 `X-Forwarded-Host` 分流请求，直连流量默认按收到的 `Host` 匹配。

适合以下场景：CDN 或边缘代理（Cloudflare Workers、负载均衡器、另一台 Caddy）将多个公网域名转发到同一个源站，路由需要按公网域名区分，同时不能采信客户端可以自行设置的请求头。

## 与内置 `header` 匹配器的关系

`@app header X-Forwarded-Host app.example.com` 不检查来源，直接采信这个请求头。但任何能直连源站的客户端都能自行设置 `X-Forwarded-Host`。本模块默认只在 Caddy 已判定对端是可信代理时使用它，复用现有的 [`trusted_proxies`](https://caddyserver.com/docs/caddyfile/options#trusted-proxies) 配置，不另设一套信任配置。下文提到的 `trust_any_peer` 是唯一的例外，必须显式开启。

## 安装

```bash
xcaddy build --with github.com/kuoruan/caddy-forwarded-host
```

## 前置条件

- `trusted_proxies` 中必须包含代理的地址。不配置也可以使用，此时本模块的行为与内置 `host` 匹配器一致，同样是一种有效配置。唯一的例外是 `trust_any_peer`，见[绕过对端检查](#绕过对端检查)。
- 请求仍然必须先匹配到某个站点块，而站点块按入站 `Host` 选择，不参考 `X-Forwarded-Host`。因此站点地址必须覆盖客户端实际发送的主机名（见[边界](#边界)）。

## 用法

本文中不带站点地址的代码块都是片段，需要放入站点块内使用。

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

主机名列表和通配符沿用内置 `host` 匹配器的规则：通配符必须占满整个标签，支持占位符，比较不区分大小写，请求携带的端口会被忽略。配置中的主机名不要包含端口：带端口的条目可以正常加载，但永远不会匹配。

```caddyfile
@app forwarded_host app.example.com app2.example.com
```

取值只看最后一个 `X-Forwarded-Host` 字段中按逗号分隔后最右侧的一项，即距离本机最近的一跳写入的值。这一项为空或不是合法的 `host[:port]` 时，请求不匹配。不会向左查找其他值，也不会回退到入站 `Host`。

### 把命中的主机名传给后端

除非配置另行修改，后端收到的是源站 `Host`。如果命中的主机名只能在运行时确定（`*.example.com` 或列表），可以引用本匹配器在匹配成功后设置的变量：

```caddyfile
@apps forwarded_host *.example.com

handle @apps {
	reverse_proxy 127.0.0.1:8081 {
		header_up Host {http.vars.forwarded_host}
	}
}
```

`{http.vars.forwarded_host}` 是本次匹配实际使用的主机名和端口（如有）：对端可信（或开启了 `trust_any_peer`）时取自 `X-Forwarded-Host`，否则取自入站 `Host`，端口原样保留。

- 只有本匹配器匹配成功时才会写入，因此只能在这条路由内使用。在兜底路由（catch-all）中读取时，它是空的，或者仍保留同一请求中上一次匹配成功写入的值。把空值传给 `header_up Host` 后，Go 会回退到上游地址，后端收到的是 `127.0.0.1:8081` 这类值，而不是公网域名。
- 同一条路由中后续的 `vars` 指令可以覆盖它；但 `vars` 匹配器不能与它在同一个匹配器集合中读取，集合内各匹配器的执行顺序没有保证。
- 用 `not` 包裹这个匹配器时，内部匹配成功仍会写入这个变量，因为取反发生在本模块执行之后。
- 它只是反映请求中已有的值，不能作为授权依据。

### 严格模式

默认情况下，请求未携带 `X-Forwarded-Host`，或该头全为空白时，会回退到入站 `Host`，因此同一个站点块可以同时服务直连和代理流量。加上 `require_header` 后，只接受来自可信代理且携带有效 `X-Forwarded-Host` 的请求（开启 `trust_any_peer` 后只剩后一个条件）：

```caddyfile
@app forwarded_host app.example.com {
	require_header
}
```

主机名写在匹配器所在行，块内只接受 `require_header` 和 `trust_any_peer`。

| 请求情况 | 默认 | `require_header` |
| --- | --- | --- |
| 对端不可信，头内容任意 | 按 `Host` 匹配 | 不匹配 |
| 对端可信，头缺失或全为空白 | 按 `Host` 匹配 | 不匹配 |
| 对端可信，头有效 | 按该值匹配 | 按该值匹配 |
| 对端可信，头有效但未匹配 | 不匹配，不回退 `Host` | 不匹配 |
| 对端可信，头有内容但无效 | 不匹配 | 不匹配 |

上表假设 `trust_any_peer` 未开启；开启后，「对端不可信」对应的行按可信处理（见[绕过对端检查](#绕过对端检查)）。

### 绕过对端检查

如果源站只允许代理访问，而代理地址无法写成网段（例如出口地址会变化），可以使用 `trust_any_peer`。开启后，匹配器不再检查对端来源，一律采用 `X-Forwarded-Host`：

```caddyfile
@app forwarded_host *.example.com {
	trust_any_peer
}
```

它只影响本匹配器。`client_ip`、访问日志和 `reverse_proxy` 仍然沿用 Caddy 根据 `trusted_proxies` 得出的判断，本模块不会把这个判断写回去。

开启这个选项后，任何能建立连接的客户端都可以通过设置 `X-Forwarded-Host` 来选择本匹配器覆盖的某条路由；值校验只能拒绝格式不合法的值。只有在入口被客户端无法伪造的手段限制为仅允许代理访问时才应开启，例如防火墙规则、socket 文件权限或带认证的 sidecar；同时代理必须覆写客户端自带的 `X-Forwarded-Host`，不能原样透传。仅监听私网地址并不充分，同网段的其他主机同样能伪造这个请求头。能列出代理网段时，配置网段仍是更稳妥的做法。

开启后对端不再参与判断，`require_header` 只剩「必须携带有效的 `X-Forwarded-Host`」这一个条件：

| 开启 `trust_any_peer` 时 | 默认 | `require_header` |
| --- | --- | --- |
| 头缺失或全为空白 | 按 `Host` 匹配 | 不匹配 |
| 头有效 | 按该值匹配 | 按该值匹配 |
| 头有效但未匹配 | 不匹配，不回退 `Host` | 不匹配 |
| 头有内容但无效 | 不匹配 | 不匹配 |

### JSON

```json
{
  "forwarded_host": {
    "hosts": ["app.example.com", "app2.example.com"]
  }
}
```

需要严格模式时加上 `"require_header": true`；需要接受任意来源的请求头时加上 `"trust_any_peer": true`。

## 使用场景

### 多个公网域名，一个源站

代理将多个租户域名转发到同一个源站，租户名单只在运行时才能确定，而给每个租户单独签发证书并不可行。

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

这样只需要一个站点块和一张 `origin.example.com` 的证书；后端收到的 `Host` 就是租户主机名。

### 用 Cloudflare Worker 改写 `Host`

Worker 会把 `Host` 改写为源站域名，原域名只保留在单个 `X-Forwarded-Host` 中。把 Cloudflare 的网段加入 `trusted_proxies`（也可以由 `http.ip_sources.cloudflare` 提供，来自 [`github.com/WeidiDeng/caddy-cloudflare-ip`](https://github.com/WeidiDeng/caddy-cloudflare-ip)，代价是多一个构建依赖），然后按第一个示例匹配公网域名。

### 迁移：新旧域名并存

```caddyfile
@both forwarded_host app.example.com app.example.net
handle @both {
	reverse_proxy 127.0.0.1:8081 {
		header_up Host {http.vars.forwarded_host}
	}
}
```

两个域名都转发到同一个后端，各自保留自己的 `Host`；下线旧域名时只需修改一个值，不必复制一份路由。

### 按公网域名分流到不同后端

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

分流依据是通过信任检查的值。灰度、蓝绿或按应用拆分时，只需替换 `handle` 块，证书和站点地址都不必改动。

### 同一站点内直连与代理并存

```caddyfile
# 直连访客照常可用：入站 Host 就是公网域名。
@direct forwarded_host app.example.com
handle @direct {
	reverse_proxy 127.0.0.1:8081
}

# 只放行代理流量：要求对端可信且 X-Forwarded-Host 有效。
@proxied forwarded_host app.example.com {
	require_header
}
handle @proxied {
	reverse_proxy 127.0.0.1:8081
}
```

只对绝不允许直连的域名启用 `require_header`。

### 不适用场景：多个域名共用一个后端，无需分流

```caddyfile
a.example.com, b.example.com {
	reverse_proxy 127.0.0.1:8081
}
```

如果没有任何逻辑依赖实际使用的域名，站点地址列表就足够。使用本模块会让配置隐含一层对 `trusted_proxies` 的依赖，也无法让第三个域名支持直连，因为它改变不了站点选择。

## 边界

- 本匹配器只在已选中的站点块内决定路由，不改变站点选择或 TLS 证书配置。想让 `app.example.com` 也能直连，站点地址就必须覆盖它（`*.example.com` 或域名列表），通常意味着需要通过 DNS-01 签发泛域名证书。
- 它不会改写 `r.Host`。要让后端看到其他值，请使用 `header_up Host {http.vars.forwarded_host}`（或指定固定值）。
- 它不是授权机制。信任 Cloudflare 的网段并不等于信任 Worker 写入这个请求头的值；如果代理将客户端自带的 `X-Forwarded-Host` 原样透传，`require_header` 也无法拦截。
- 只处理 `X-Forwarded-Host`。`X-Forwarded-For` 由 `trusted_proxies` / `client_ip` 负责；`X-Forwarded-Proto` 与主机匹配无关，也不会还原 TLS/SNI。
- 主机名中的占位符只适合静态来源（环境变量或配置文件）。请求派生的占位符，例如 `forwarded_host {http.request.header.X-Tenant-Host}`，等于允许请求向匹配器中注入通配符；这种用法不受支持，也未做加固。
- 带尾点的 FQDN（`app.example.com.`）可以通过校验，但不会匹配 `app.example.com`，也不会回退到 `Host`。
- 静态文件服务和共享缓存的键是 `Host` + URL，而不是 `X-Forwarded-Host`。如果响应会在多个主机之间共享，需要发送 `Vary: X-Forwarded-Host`，或者关闭缓存。

## 故障排查

「对端可信、`X-Forwarded-Host` 有内容但不合法」的情况最难排查：匹配器不回退，请求会表现为普通的 404，模块本身也不输出日志。可以按以下步骤缩小范围：

1. 从 `trusted_proxies` 内的地址发送一个带有这个请求头的请求（本地测试时可以临时加入自己使用的地址）：`curl -H 'Host: origin.example.com' -H 'X-Forwarded-Host: app.example.com' https://origin.example.com/`
2. 查看 Caddy 的访问日志。请求头会记录在日志中，每条日志也会记录处理这个请求的站点块。
3. 再发送一次不带这个请求头的相同请求作为对照：如果这次能够匹配，问题就出在请求头的值上（最右一项为空、带尾点，或是完整 URL 这类不是 `host[:port]` 的值）。

## 兼容范围

构建基线为 Caddy v2.10.2（Go 1.25），已在 v2.11.9 上实测通过。

使用 Go 1.27+ 构建的 Caddy 2.10.x 无法加载任何配置，原因是上游 `encoding/json` 的改动（`json.RawMessage` 成为别名）。在 Go 1.27+ 上请使用 Caddy 2.11.x，或改用 Go 1.25/1.26 构建 2.10.x。细节见 [CONTEXT.md](CONTEXT.md)。
