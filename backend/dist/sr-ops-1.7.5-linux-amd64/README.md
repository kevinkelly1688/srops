# sr-ops · StarRocks 运维平台服务端（Go）

独立部署的单个二进制：托管前端页面，并提供 `/api`。已实现**集群导入**、**集群总览**、**集群架构**、**性能巡检**、**查询分析**（含 SQL 审计）、**资源隔离**、**导入作业**、**物化视图**、**备份恢复**、**参数配置**、**告警中心**、**节点管理**、**安装部署**、**升级回滚**、**用户管理**、**角色权限**、**操作审计**、**平台设置**——前端所有页面都已接入后端。节点启停、扩容、部署、升级、参数写配置文件通过 SSH 执行（后台任务，日志保存在 `data/tasks/`）。

## 目录

```
cmd/sr-ops/          服务端入口
internal/sr/         StarRocks 访问：MySQL 协议（SHOW / information_schema）+ 节点 HTTP /metrics 解析
internal/collect/    集群架构、总览、趋势（Prometheus 或平台内置 15 秒采样）
internal/inspect/    巡检引擎：27 条规则、阈值覆盖、忽略项、健康分、定时巡检
internal/perm/       权限模型：权限树（生成）、角色合并、数据范围、防越权判断
internal/mail/       SMTP 邮件发送（SSL / STARTTLS / 不加密）
internal/notify/     通知发送：企业微信 / 钉钉 / 飞书 / Lark / Slack / Webhook（签名、业务返回码校验）
internal/sshx/       SSH（平台 ed25519 密钥、首次信任主机指纹）
internal/store/      元数据文件存储（state.json、巡检报告、审计日志）
internal/secure/     AES-256-GCM 加密集群 / SSH 密码，PBKDF2 用户密码
internal/api/        HTTP 接口、会话、权限、静态文件白名单；e2e_test.go 为端到端测试
fakesr/              假 StarRocks 集群（MySQL 协议 + /metrics + SSH），用于本地开发与测试
deploy/              systemd 单元
```

## 本地运行

> 本机全局 Go 配置中 `GOTOOLCHAIN` 无效时，命令前加 `GOTOOLCHAIN=local`。

```bash
# 1. 启动假 StarRocks（存算一体、问题场景；MySQL 19030，SSH 12222，账号 sr_ops / secret）
cd fakesr && go run ./cmd/fakesr -scenario problems

# 2. 启动平台（另开终端，在 backend 目录）
go run ./cmd/sr-ops -listen 127.0.0.1:8767 -data data -web ..
```

要演示节点启停、日志、扩容、部署、升级，需要让每个节点有独立地址与 SSH 主机，用 `-hosts` 模式（SSH 端口必须固定，`-spare` 是额外的空白主机，用于部署 / 扩容）：

```bash
cd fakesr && go run ./cmd/fakesr -hosts -ssh-port 12333 -spare 3 -scenario healthy
# 节点地址 127.0.0.11 起，空白主机 127.0.0.50 起（Windows / Linux 的回环网段都可直接使用）；MySQL 仍是 127.0.0.1:19030
# 导入集群时 SSH 用户 starrocks、端口 12333、密码 secret，FE 目录 /opt/starrocks/fe，BE / CN 目录 /opt/starrocks/be
# 备份快照清理演示：再加 -s3-port 19190 启动内存 S3（访问密钥 ID fakeak，路径风格），仓库 repo_s3_prod 的快照与其中的文件联动
# 演示用假安装包在 fakesr/demo-pkgs/（3.5.6、3.5.8 与假 JDK），到「安装部署 → 安装包」上传；添加主机 127.0.0.50-52，用密码 secret
```

浏览器打开 http://127.0.0.1:8767/ ，用 `admin` 和 `data/initial_admin_password` 中的初始密码登录，首次登录必须修改密码。然后在引导页「导入已有集群」：FE 地址 `127.0.0.1`、query_port `19030`、http_port `18030`、运维账号 `sr_ops` / `secret`；SSH 用户 `starrocks`、端口 `12222`、认证方式选密码 `secret`。

## 启动参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `-listen` | `127.0.0.1:8766` | 监听地址；对外提供服务改为 `0.0.0.0:8766` 并放在 HTTPS 反向代理之后 |
| `-data` | `data` | 数据目录：`state.json`、`reports/`、`audit.jsonl`、`secret.key`、`ssh/`。**务必备份 `secret.key`**，丢失后已保存的集群密码无法解密 |
| `-web` | `..` | 前端目录（含 `index.html`、`css/`、`js/`） |
| `-trusted-proxies` | 空 | 可信反向代理的 IP / CIDR；只有来自这些地址的请求才采信 `X-Forwarded-For`（影响审计来源 IP 与 IP 白名单） |

## 配置文件（sr-ops.conf）

一键脚本安装后，启动配置在 `<安装目录>/sr-ops.conf`（systemd 的 `EnvironmentFile`），修改后执行 `systemctl restart sr-ops` 生效，升级时原样保留。除监听地址等启动参数外，还有两项：

| 配置项 | 默认 | 说明 |
|---|---|---|
| `SR_EXPIRE` | `2028-01-01` | 使用期限（含当天）。到期后平台照常运行，页面不提醒，只在日志里记录；日期格式不对时按默认值处理并记一条日志 |
| `SR_ICP` | 空 | ICP 备案号，显示在登录页与主界面底部，链接到工信部备案查询网站；留空则不显示 |

早期安装的 `sr-ops.conf` 里没有这两项，期限按默认值，需要显示备案号时手工加一行 `SR_ICP=...`。不用安装脚本、直接运行二进制时，设置同名环境变量即可。

## 测试

```bash
go vet ./... && go test ./...          # 含端到端测试：用 fakesr 启动假集群，走完登录 → 导入 → 总览 → 架构 → 巡检
cd fakesr && go test ./...
```

## 部署（一键脚本）

安装包里带有 `install.sh`，在解压后的目录里以 root 运行：`./install.sh install | upgrade | rollback | uninstall | status`（选项见 `./install.sh --help`，说明见使用手册「平台部署与集群导入」）。升级前自动备份、拒绝降级、健康检查失败自动回滚；卸载默认保留数据目录。

## 打包

```bash
bash build.sh linux-amd64     # 或 linux-arm64 / windows-amd64
```

生成 `dist/sr-ops-<版本>-<平台>.tar.gz`（二进制 + `web/` + `sr-ops.service`）。安装步骤见使用手册「平台部署与集群导入」。

## 安全要点

- 集群密码、SSH 密码、短信网关密钥、备份仓库的清理凭据以 AES-256-GCM 加密保存；接口不返回密文。
- 用户密码 PBKDF2-SHA256（21 万次迭代）；连续 5 次失败锁定 30 分钟；会话 30 分钟无操作过期。
- 写操作要求请求头 `X-Requested-With: sr-ops`，Cookie 为 HttpOnly + SameSite=Lax（防 CSRF）。
- 静态文件白名单：只放行 `index.html`、`css/*.css`、`js/*.js` 与用户手册。
- SSH 主机指纹首次信任后记录在 `data/ssh/known_hosts`，指纹变化即拒绝连接。
- 通过 SSH 下发的命令只拼入经过白名单校验的目录、主机、版本号与端口；配置内容通过标准输入写入文件，不经过 shell；上传的安装包只接受固定的文件名格式并检查压缩包内容。
- 添加主机时的登录密码只用于首次写入平台公钥，不落盘、不写日志；部署的 root 密码、对象存储密钥在任务日志中隐藏，保存在任务里的部署参数用平台密钥加密。
- 部署目录、主机名、审计表名均做白名单校验后才拼入命令 / SQL。
- 定时巡检通知的 Webhook 地址与签名密钥加密保存，接口只返回脱敏地址；发送错误信息中不含 token。境外地址需代理时设置环境变量 `HTTPS_PROXY`。
- 权限在服务端按权限码校验（`internal/perm`，权限树由 `tools/build_help.py` 从前端 `js/perm.js` 生成，测试会校验两者一致）；用户与角色变更立即生效。只能授予 / 管理权限不超过自己的角色与用户（防越权）；`admin` 不可锁定、删除；至少保留一个可用的超级管理员。
