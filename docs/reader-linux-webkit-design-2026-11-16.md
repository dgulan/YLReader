# Linux WebKit同构对照设计

沿用冻结API/DDL、当前amd64前端镜像和当前Linux后端、原创TXT/EPUB；不修改应用src、不部署/提交/推送、不开放局域网。保持15秒登录/正文检查、CSP/pageerror/未处理拒绝空断言和原始阅读流程，不预热、不重试至成功。

使用官方Playwright 1.65.0 Noble amd64镜像，固定manifest摘要 `sha256:f6fe3cf9a493670b9a20f33597a807262abb25c40c31839ba4ac1dbc51fe7366`，与本机锁定SDK版本一致。官方镜像包含浏览器与系统依赖而不包含SDK，测试仅只读挂载本机playwright-core纯JS包启动run-server，不安装新npm依赖、不挂Docker socket或生产数据。依据[官方容器说明](https://playwright.dev/docs/docker)核对镜像/SDK版本匹配。

浏览器容器与本轮前端共享网络命名空间，通过http://127.0.0.1访问真实Nginx；前端额外发布一个127.0.0.1随机控制端口给Windows Playwright客户端。控制路径含本轮ID、最多一个客户端。前端配置/dist不覆盖；临时PG/Redis/API保持独占，结束按唯一标签逆序清理。

浏览器镜像为Linux amd64，与Docker引擎x86_64匹配，不使用QEMU。记录容器平台/架构、Node和SDK版本、镜像ID、后端及样本摘要。Windows客户端执行测试，Linux服务执行浏览器；此对照同时改变引擎平台和部分网络路径，不能仅凭通过断言底层根因已唯一定位。

浏览器使用只读根文件系统、独立tmpfs/HOME、内存/CPU/PID/共享内存限制和无额外capabilities；先尝试镜像非root用户。只浏览本轮原创/合成数据，不测试远程书源。如果运行限制导致启动失败，应记录并修正必要配置，不关闭CSP或降低原断言。

先执行完整阅读两项及HTTP门禁；若通过，再补合成生命周期回归，以区分真实业务与网络等待。Windows已有失败不删除，Linux容器不替代iPhone15 Pro/iOS27.2.1/Safari真机、长期阅读或arm64原生验收。第3阶段整体状态仍按全部门禁判断。

因原现象具有偶发性，固定执行两轮独立临时环境的完整阅读和一轮19项生命周期；不根据失败追加重试至成功。阅读、登录、生命周期和HTTP-only分别标记verification_mode，不能混用模式宣称完整阅读通过。保留本地Windows Edge默认设置，远程浏览器不强制使用Windows专属channel。
