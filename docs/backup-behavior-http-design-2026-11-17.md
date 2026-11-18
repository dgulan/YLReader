# 受限HTTPS行为运输层契约

先行内部协议，不修改冻结API/DDL、账本或合成报告profile；不创建公开执行CLI/API，不启动生产服务。

## 目标与网络

只接受显式 `https://127.0.0.1:port` 或 `https://[::1]:port`，可有单独根斜线，不允许hostname/DNS、userinfo、其他路径、query、fragment、代理或环境配置回退。可信装配者须独立提供目标实例/数据库/运行修订绑定、服务器叶证书DER、目标JWT密钥、隔离DSN及source/target数据库pin、授权user UUID集合。证书pin不是数据库配置证明；谁把证书与目标绑定仍是装配信任边界，不能从任意远端健康响应生成。

TLS至少1.2，固定叶证书完整DER的SHA256并校验IP SAN/证书时效。自建固定pin验证替代公共CA链，不接受回退；收到其他证书时在HTTP凭据发送前拒绝。拨号只允许预定literal地址/端口。关闭代理、cookie jar、压缩、HTTP2与keep-alive，拒绝重定向；POST无GetBody及幂等重试头，网络/EOF/超时不重放。单HTTP请求最多5秒，头16KiB、登录响应64KiB，无任意附加headers。

此校验仅供内部运维运输层，不改变书源fetcher的防SSRF内网拒绝规则。请求体使用独立字节副本，Read/Close同步，避免Do返回后的异步读取与清理竞争。合法输入进入请求阶段前按user锁存尝试；即使握手/响应失败也不对该用户再次发POST。

仅发POST /api/v1/auth/login及POST /api/v1/auth/logout，不发Refresh、外站请求或其他业务写入。登录仅发送username/password，不发送原JWT/refresh或源密钥。HTTP请求/响应和底层错误不进入日志、账本或错误文本。

## 响应与数据库绑定

登录要求200、application/json、无压缩、严格UTF8/JSON对象、无重复/未知字段。冻结User的nullable display_name/last_login_at按契约接受；核心字段必需且类型/长度受限。Bearer/access/refresh/用户ID和username须匹配授权；目标access须HS256目标密钥签名、有效iat/exp、正确subject及token_version。未知用户、畸形/过大响应、重定向或传输失败统一返回结果未确认。

以收到的refresh计算查库摘要，只在目标READ ONLY/REPEATABLE READ事务中核对系统/数据库/角色pin、角色权限及冻结账本，查询确切session UUID/owner、用户version/disabled、created_at/expiry及revoked。创建时刻允许请求前最多1秒、未来最多1秒的本机时钟偏差。HTTP服务可合法持有数据库连接，此处仅放宽“无其他同库连接”一项，不放宽pin、角色或只读要求；原账号核验/会话观察仍要求排他。

只有200响应验签且目标库会话匹配才返回已知UUID；登录失败后不能猜测会话或自动补救。JWT/refresh仅存内存，以UUID关联，最多8条。Close丢弃本地秘密/密钥并关闭空闲连接，不自动登出，Go字符串不承诺安全擦除。fmt/JSON输出禁止泄露秘密。

Logout仅接受本运输层记住的同用户UUID，调用前锁存该会话已尝试，不允许二次调用，即使前置核对或响应失败。先核对目标库仍是同一未撤销会话及JWT仍有效，再发送Bearer和该会话refresh；要求204空体，再只读确认同一会话revoked。204本身不是撤销证明，过期也不算撤销。错误/取消/超时不重试、不刷新access、不撤销未知或该用户全部会话。

## 验收与未完成项

纯函数/假HTTP接口测试URL、重复/nullable JSON、JWT及错误脱敏、响应上限、Close、一次登出边界；真实隔离归档夹具使用API实际TLS登录/登出验证正常、响应丢失、重定向不跟随、错误证书零凭据POST，并核对新增会话及不重试。证书/密钥仅由夹具创建，不是业务账号。

此模块只供显式可信装配，尚无生产服务器生命周期或用户配置入口；不能证明运行实例其他业务依赖全部正确，也不生成通用报告pass/切换许可。后续须连接同次行为/只读会话观察、失败保留及通用报告协议；不从磁盘恢复执行权。
