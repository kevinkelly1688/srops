#!/usr/bin/env bash
# StarRocks 运维平台（sr-ops）一键部署 / 升级 / 回滚 / 卸载
#
# 用法（在解压后的安装包目录里，以 root 运行）：
#   ./install.sh install   [选项]    全新安装并启动
#   ./install.sh upgrade   [选项]    升级到当前安装包的版本（保留数据，失败自动回滚）
#   ./install.sh rollback  [选项]    回滚到升级前备份的程序版本
#   ./install.sh uninstall [选项]    卸载（默认保留数据目录）
#   ./install.sh status              查看安装与运行状态
#
# 选项：
#   --dir DIR              安装目录（默认 /opt/sr-ops；升级 / 卸载自动识别已安装的目录）
#   --listen ADDR          监听地址（默认 0.0.0.0:8766）；--port N 只改端口
#   --user NAME            运行服务的系统用户（默认 srops，不存在会创建）
#   --trusted-proxies L    可信反向代理的 IP / CIDR，逗号分隔（平台前面有 Nginx 等代理时必须设置）
#   --https-proxy URL      出网 HTTP 代理（Lark / Slack 等境外通知渠道需要）
#   --no-start             安装后不启动
#   --no-data-backup       升级时不备份数据目录
#   --purge                卸载时同时删除数据目录、备份和系统用户（不可恢复）
#   --to TS                回滚到指定备份（备份目录名，默认最近一次）
#   --force                允许降级 / 重复安装覆盖程序文件
#   -y, --yes              不再询问确认
#   --no-systemd           不使用 systemd（只复制文件，不创建系统用户、不要求 root；用于容器 / 测试）
#
# 安装后的配置文件 <安装目录>/sr-ops.conf 保存监听地址等启动参数，修改后执行 systemctl restart sr-ops 生效；
# 升级时配置、数据目录原样保留。

set -euo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE=sr-ops
UNIT="${SR_OPS_UNIT:-/etc/systemd/system/${SERVICE}.service}"

DIR=""
RUN_USER=""
LISTEN=""
PORT=""
TRUSTED=""
HTTPS_PROXY_URL=""
NO_START=0
DATA_BACKUP=1
PURGE=0
FORCE=0
YES=0
SYSTEMD=1
TO=""
KEEP_BACKUPS=5

c_red=$'\033[31m'; c_grn=$'\033[32m'; c_ylw=$'\033[33m'; c_off=$'\033[0m'
if [ ! -t 1 ]; then c_red=""; c_grn=""; c_ylw=""; c_off=""; fi
log()  { printf '%s\n' "${c_grn}==>${c_off} $*"; }
warn() { printf '%s\n' "${c_ylw}警告：${c_off}$*" >&2; }
die()  { printf '%s\n' "${c_red}错误：${c_off}$*" >&2; exit 1; }

usage() { sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

confirm() { # confirm "问题"
  [ "$YES" = 1 ] && return 0
  local a
  read -r -p "$1 [y/N] " a || true
  case "$a" in y|Y|yes|YES) return 0 ;; esac
  return 1
}

need_root() {
  [ "$SYSTEMD" = 0 ] && return 0
  [ "$(id -u)" = 0 ] || die "需要 root 权限，请使用 sudo 运行：sudo $0 $CMD"
  command -v systemctl >/dev/null 2>&1 || die "没有找到 systemctl：本脚本依赖 systemd；容器或非 systemd 环境请加 --no-systemd 只复制文件，再自行启动 sr-ops"
}

sysctl_() { [ "$SYSTEMD" = 1 ] && systemctl "$@"; }

# ---------- 参数 ----------
CMD="${1:-}"
case "$CMD" in
  install|upgrade|rollback|uninstall|status) shift ;;
  -h|--help|help|"") usage; exit 0 ;;
  *) usage; die "未知命令：$CMD" ;;
esac
while [ $# -gt 0 ]; do
  case "$1" in
    --dir) DIR="${2:?--dir 需要参数}"; shift 2 ;;
    --listen) LISTEN="${2:?--listen 需要参数}"; shift 2 ;;
    --port) PORT="${2:?--port 需要参数}"; shift 2 ;;
    --user) RUN_USER="${2:?--user 需要参数}"; shift 2 ;;
    --trusted-proxies) TRUSTED="${2:?--trusted-proxies 需要参数}"; shift 2 ;;
    --https-proxy) HTTPS_PROXY_URL="${2:?--https-proxy 需要参数}"; shift 2 ;;
    --to) TO="${2:?--to 需要参数}"; shift 2 ;;
    --no-start) NO_START=1; shift ;;
    --no-data-backup) DATA_BACKUP=0; shift ;;
    --purge) PURGE=1; shift ;;
    --force) FORCE=1; shift ;;
    -y|--yes) YES=1; shift ;;
    --no-systemd) SYSTEMD=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知选项：$1（./install.sh --help 查看用法）" ;;
  esac
done

# ---------- 参数校验 ----------
valid_dir() { [[ "$1" =~ ^/[A-Za-z0-9._/-]+$ ]] || return 1; case "$1" in /|/usr|/etc|/bin|/sbin|/lib|/lib64|/var|/opt|/boot|/dev|/proc|/sys) return 1 ;; esac; if [ "$SYSTEMD" = 1 ]; then case "$1" in /home|/home/*|/root|/root/*|/tmp|/tmp/*) return 1 ;; esac; fi; return 0; }
[[ -z "$PORT" || "$PORT" =~ ^[0-9]+$ ]] || die "--port 必须是数字"
[ -z "$PORT" ] || { [ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ]; } || die "--port 须为 1–65535"
[[ -z "$RUN_USER" || "$RUN_USER" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "--user 只能是小写字母、数字、下划线或连字符"
[[ -z "$LISTEN" || "$LISTEN" =~ ^[A-Za-z0-9.:_\[\]-]+$ ]] || die "--listen 格式不正确，例如 0.0.0.0:8766"
[[ -z "$TRUSTED" || "$TRUSTED" =~ ^[0-9A-Fa-f.:/,]+$ ]] || die "--trusted-proxies 只能是 IP / CIDR，用逗号分隔"
[[ -z "$HTTPS_PROXY_URL" || "$HTTPS_PROXY_URL" =~ ^(https?|socks5)://[^[:space:]\"\'\\\$\`]+$ ]] || die "--https-proxy 格式应为 http://主机:端口"
[[ -z "$TO" || "$TO" =~ ^[0-9]{8}-[0-9]{6}$ ]] || die "--to 应为备份目录名，如 20261009-153000"

# ---------- 已安装信息 ----------
unit_value() { [ -f "$UNIT" ] && sed -n "s/^$1=//p" "$UNIT" | head -1 || true; }
conf_value() { [ -f "$DIR/sr-ops.conf" ] && sed -n "s/^$1=//p" "$DIR/sr-ops.conf" | head -1 || true; }
read_version() { [ -f "$1" ] && tr -d 'v \r\n' < "$1" || echo "未知"; }

# 兼容按手册手工安装的旧版本（没有 sr-ops.conf）：从现有 unit 的 ExecStart 提取启动参数生成配置
migrate_legacy() {
  [ -x "$DIR/sr-ops" ] && [ ! -f "$DIR/sr-ops.conf" ] && [ -f "$UNIT" ] || return 0
  local es l t
  es="$(sed -n 's/^ExecStart=//p' "$UNIT" | head -1)"
  l="$(printf '%s' "$es" | sed -n 's/.*-listen \([^ ]*\).*/\1/p')"
  t="$(printf '%s' "$es" | sed -n 's/.*-trusted-proxies \([^ ]*\).*/\1/p')"
  LISTEN="${l:-0.0.0.0:8766}"; TRUSTED="$t"
  HTTPS_PROXY_URL="$(sed -n 's/^Environment=HTTPS_PROXY=//p' "$UNIT" | head -1)"
  log "检测到手工安装的旧版本，从 $UNIT 提取启动参数生成 $DIR/sr-ops.conf（监听 $LISTEN）"
  gen_conf "$DIR/sr-ops.conf"
  if [ "$SYSTEMD" = 1 ]; then chown root:"$RUN_USER" "$DIR/sr-ops.conf"; chmod 640 "$DIR/sr-ops.conf"; fi
}

detect_installed() {
  if [ -z "$DIR" ]; then
    local d; d="$(unit_value WorkingDirectory)"
    DIR="${d:-/opt/sr-ops}"
  fi
  valid_dir "$DIR" || die "安装目录不合法：$DIR（需要绝对路径，只含字母数字 ._/-；不能是系统目录，也不能在 /home、/root、/tmp 下——systemd 沙箱 ProtectHome 会拦截）"
  if [ -z "$RUN_USER" ]; then
    local u; u="$(unit_value User)"
    RUN_USER="${u:-srops}"
  fi
}

installed() { [ -x "$DIR/sr-ops" ] && [ -f "$DIR/sr-ops.conf" ]; }

listen_port() { local l; l="$(conf_value SR_LISTEN)"; echo "${l##*:}"; }

health() { # health PORT → 打印版本，失败返回 1
  local port="$1" body
  if command -v curl >/dev/null 2>&1; then
    body="$(curl -fs --max-time 2 "http://127.0.0.1:${port}/api/health" 2>/dev/null)" || return 1
    printf '%s' "$body" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p'
    return 0
  fi
  (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null && echo "?" || return 1
}

wait_health() { # wait_health PORT 秒数
  local port="$1" i
  for ((i = 0; i < $2; i++)); do
    if health "$port" >/dev/null; then return 0; fi
    sleep 1
  done
  return 1
}

port_in_use() {
  if command -v ss >/dev/null 2>&1; then ss -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]$1\$"; return; fi
  if command -v netstat >/dev/null 2>&1; then netstat -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]$1\$"; return; fi
  return 1
}

check_package() {
  [ -f "$SRC/sr-ops" ] && [ -d "$SRC/web" ] || die "当前目录不是 sr-ops 安装包（缺少 sr-ops 或 web/）：$SRC"
  [ -f "$SRC/VERSION" ] || warn "安装包里没有 VERSION 文件，版本号显示为“未知”"
  chmod +x "$SRC/sr-ops" 2>/dev/null || true
  local rc=0
  "$SRC/sr-ops" -h >/dev/null 2>&1 || rc=$?
  if [ "$rc" -ge 126 ]; then
    die "安装包里的 sr-ops 无法在本机运行（退出码 $rc）：请确认安装包的 CPU 架构与本机一致（本机：$(uname -m)；amd64 对应 x86_64，arm64 对应 aarch64）"
  fi
}

gen_conf() { # gen_conf 文件
  {
    echo "# sr-ops 启动参数，修改后执行 systemctl restart sr-ops 生效"
    echo "SR_LISTEN=$LISTEN"
    echo "SR_ARGS=${TRUSTED:+-trusted-proxies $TRUSTED}"
    echo "# 使用期限（含当天）：到期后平台照常运行，页面不提醒，只在日志中记录"
    echo "SR_EXPIRE=2028-01-01"
    echo "# 登录页与主界面底部展示的 ICP 备案号，留空则不展示"
    echo "SR_ICP="
    if [ -n "$HTTPS_PROXY_URL" ]; then
      echo "HTTPS_PROXY=$HTTPS_PROXY_URL"
      echo "HTTP_PROXY=$HTTPS_PROXY_URL"
    fi
  } > "$1"
}

gen_unit() {
  cat > "$UNIT" <<EOF
[Unit]
Description=StarRocks 运维平台 (sr-ops)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$RUN_USER
Group=$RUN_USER
WorkingDirectory=$DIR
EnvironmentFile=$DIR/sr-ops.conf
ExecStart=$DIR/sr-ops -listen \${SR_LISTEN} -data $DIR/data -web $DIR/web \$SR_ARGS
Restart=on-failure
RestartSec=5
LimitNOFILE=65535
# 平台只需要读写自己的数据目录
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$DIR/data
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
  chmod 644 "$UNIT"
}

copy_program() { # copy_program：把安装包里的程序文件复制到安装目录（不动 data / 配置 / 备份）
  rm -rf "$DIR/web.new"
  cp -r "$SRC/web" "$DIR/web.new"
  install -m 0755 "$SRC/sr-ops" "$DIR/sr-ops.new"
  [ -f "$SRC/VERSION" ] && install -m 0644 "$SRC/VERSION" "$DIR/VERSION.new"
  [ -f "$SRC/README.md" ] && install -m 0644 "$SRC/README.md" "$DIR/README.md"
  install -m 0755 "$SRC/install.sh" "$DIR/install.sh.new"
  rm -rf "$DIR/web"
  mv "$DIR/web.new" "$DIR/web"
  mv -f "$DIR/sr-ops.new" "$DIR/sr-ops"
  [ -f "$DIR/VERSION.new" ] && mv -f "$DIR/VERSION.new" "$DIR/VERSION"
  mv -f "$DIR/install.sh.new" "$DIR/install.sh"
  if [ "$SYSTEMD" = 1 ]; then
    chown -R root:root "$DIR/web" "$DIR/sr-ops" "$DIR/install.sh"
    command -v restorecon >/dev/null 2>&1 && restorecon -R "$DIR" >/dev/null 2>&1 || true
  fi
}

default_ip() { hostname -I 2>/dev/null | awk '{print $1}' || true; }

print_summary() {
  local port ip
  port="$(listen_port)"; ip="$(default_ip)"
  echo
  echo "  访问地址：http://${ip:-<服务器地址>}:${port}/"
  echo "  管理员：  admin（首次登录必须修改初始密码）"
  if [ -f "$DIR/data/initial_admin_password" ]; then
    echo "  初始密码：$(cat "$DIR/data/initial_admin_password")   （文件：$DIR/data/initial_admin_password，修改密码后自动删除）"
  fi
  echo "  安装目录：$DIR    数据目录：$DIR/data（请定期备份，尤其是 secret.key）"
  echo "  常用命令：systemctl status|restart|stop $SERVICE ；journalctl -u $SERVICE -f"
  echo "  升级 / 卸载：$DIR/install.sh upgrade|uninstall（在新版本安装包目录里运行 ./install.sh upgrade）"
  echo "  生产环境建议在前面加 Nginx 提供 HTTPS，并设置 --trusted-proxies；防火墙需放行 ${port}/tcp。"
}

# ---------- install ----------
do_install() {
  need_root
  DIR="${DIR:-/opt/sr-ops}"; RUN_USER="${RUN_USER:-srops}"
  valid_dir "$DIR" || die "安装目录不合法：$DIR（需要绝对路径，只含字母数字 ._/-；不能是系统目录，也不能在 /home、/root、/tmp 下——systemd 沙箱 ProtectHome 会拦截）"
  check_package
  if installed && [ "$FORCE" = 0 ]; then
    die "$DIR 已经安装了 sr-ops（版本 $(read_version "$DIR/VERSION")）。升级请运行：./install.sh upgrade；确需覆盖程序文件请加 --force"
  fi
  [ -n "$LISTEN" ] || LISTEN="0.0.0.0:${PORT:-8766}"
  local port="${LISTEN##*:}"
  if ! installed && port_in_use "$port"; then
    die "端口 $port 已被占用，请用 --port 换一个端口"
  fi

  log "安装 sr-ops $(read_version "$SRC/VERSION") 到 $DIR"
  if [ "$SYSTEMD" = 1 ] && ! id "$RUN_USER" >/dev/null 2>&1; then
    log "创建系统用户 $RUN_USER"
    useradd -r -s /sbin/nologin -d "$DIR" "$RUN_USER"
  fi
  mkdir -p "$DIR/data"
  copy_program
  if [ ! -f "$DIR/sr-ops.conf" ]; then gen_conf "$DIR/sr-ops.conf"; fi
  if [ "$SYSTEMD" = 1 ]; then
    chown root:"$RUN_USER" "$DIR/sr-ops.conf"; chmod 640 "$DIR/sr-ops.conf"
    chown "$RUN_USER":"$RUN_USER" "$DIR/data"; chmod 700 "$DIR/data"
    gen_unit
    systemctl daemon-reload
    systemctl enable "$SERVICE" >/dev/null 2>&1
    if [ "$NO_START" = 0 ]; then
      log "启动服务"
      systemctl restart "$SERVICE"
      if wait_health "$port" 30; then
        log "服务已启动（版本 $(health "$port")）"
      else
        warn "30 秒内没有检测到服务响应，请查看日志：journalctl -u $SERVICE -n 50 --no-pager"
        systemctl --no-pager status "$SERVICE" 2>&1 | tail -8 || true
        exit 1
      fi
    else
      log "已安装，未启动（--no-start）；启动：systemctl start $SERVICE"
    fi
  else
    log "已复制文件（--no-systemd）；启动命令：$DIR/sr-ops -listen $LISTEN -data $DIR/data -web $DIR/web"
  fi
  print_summary
}

# ---------- 备份 ----------
backup_root() { echo "$DIR/backup"; }

make_backup() { # make_backup TS
  local ts="$1" b; b="$(backup_root)/$ts"
  mkdir -p "$b"
  cp -a "$DIR/sr-ops" "$b/sr-ops"
  cp -a "$DIR/web" "$b/web"
  [ -f "$DIR/VERSION" ] && cp -a "$DIR/VERSION" "$b/VERSION"
  [ -f "$DIR/sr-ops.conf" ] && cp -a "$DIR/sr-ops.conf" "$b/sr-ops.conf"
  [ -f "$UNIT" ] && cp -a "$UNIT" "$b/sr-ops.service"
  if [ "$DATA_BACKUP" = 1 ] && [ -d "$DIR/data" ]; then
    log "备份数据目录（不含 packages/ 与 tasks/）到 $b/data.tgz"
    tar -czf "$b/data.tgz" --exclude='data/packages' --exclude='data/tasks' -C "$DIR" data
    chmod 600 "$b/data.tgz"
  fi
  # 只保留最近 KEEP_BACKUPS 份
  local n=0 d
  for d in $(ls -1 "$(backup_root)" 2>/dev/null | sort -r); do
    n=$((n + 1))
    [ "$n" -gt "$KEEP_BACKUPS" ] && rm -rf "$(backup_root)/$d"
  done
  return 0
}

restore_program() { # restore_program TS
  local b; b="$(backup_root)/$1"
  [ -d "$b" ] && [ -f "$b/sr-ops" ] || die "找不到备份：$b"
  rm -rf "$DIR/web" && cp -a "$b/web" "$DIR/web"
  install -m 0755 "$b/sr-ops" "$DIR/sr-ops"
  [ -f "$b/VERSION" ] && install -m 0644 "$b/VERSION" "$DIR/VERSION"
  [ -f "$b/sr-ops.conf" ] && cp -a "$b/sr-ops.conf" "$DIR/sr-ops.conf"
  if [ "$SYSTEMD" = 1 ]; then
    chown -R root:root "$DIR/web" "$DIR/sr-ops"
    [ -f "$b/sr-ops.service" ] && cp -a "$b/sr-ops.service" "$UNIT" && systemctl daemon-reload
  fi
}

# ---------- upgrade ----------
do_upgrade() {
  need_root
  detect_installed
  check_package
  migrate_legacy
  installed || die "没有在 $DIR 找到已安装的 sr-ops：全新安装请运行 ./install.sh install"
  if [ "$SRC" -ef "$DIR" ]; then
    die "请在新版本的安装包目录里运行（解压新的 tar.gz 后执行 ./install.sh upgrade），不要运行已安装目录里的脚本"
  fi
  local old new
  old="$(read_version "$DIR/VERSION")"; new="$(read_version "$SRC/VERSION")"
  if [ "$old" = "$new" ] && [ "$FORCE" = 0 ]; then
    die "已经是 $new 版本。需要重新覆盖请加 --force"
  fi
  if [ "$old" != "未知" ] && [ "$new" != "未知" ] && [ "$FORCE" = 0 ] && [ "$(printf '%s\n%s\n' "$old" "$new" | sort -V | tail -1)" != "$new" ]; then
    die "新版本 $new 低于已安装的 $old。降级可能与已有数据不兼容，确需降级请加 --force（建议先备份）"
  fi
  local port; port="$(listen_port)"
  [ -n "$port" ] || port=8766

  echo "即将升级 sr-ops：$old → $new（安装目录 $DIR）"
  echo "  · 升级会短暂停止服务；正在执行的后台任务（节点启停、部署、升级等）会被中断，请确认没有进行中的任务"
  echo "  · 数据目录和配置保留；升级前会自动备份程序与数据到 $(backup_root)/<时间>/"
  confirm "继续升级？" || die "已取消"

  local ts; ts="$(date +%Y%m%d-%H%M%S)"
  log "停止服务"
  sysctl_ stop "$SERVICE" || true
  make_backup "$ts"
  log "替换程序文件"
  copy_program
  if [ "$SYSTEMD" = 1 ]; then
    gen_unit            # 刷新 unit（沿用 sr-ops.conf 中的启动参数）
    systemctl daemon-reload
    [ -n "$LISTEN$TRUSTED$HTTPS_PROXY_URL$PORT" ] && warn "升级时忽略 --listen / --port / --trusted-proxies / --https-proxy：请直接修改 $DIR/sr-ops.conf"
    log "启动服务"
    systemctl start "$SERVICE" || true
    if wait_health "$port" 30; then
      log "升级完成：$old → $new（运行版本 $(health "$port")）"
      echo "  备份保存在 $(backup_root)/$ts/ ；如需回退：$DIR/install.sh rollback"
      return 0
    fi
    warn "新版本没有在 30 秒内正常启动，自动回滚到 $old"
    systemctl stop "$SERVICE" >/dev/null 2>&1 || true
    restore_program "$ts"
    systemctl start "$SERVICE" || true
    if wait_health "$port" 30; then
      die "升级失败，已回滚到 $old 并恢复服务。请查看日志：journalctl -u $SERVICE -n 80 --no-pager"
    fi
    die "升级失败，回滚后服务仍未恢复，请手工检查：journalctl -u $SERVICE -n 80 --no-pager ；备份在 $(backup_root)/$ts/"
  fi
  log "已替换程序文件（--no-systemd），请自行重启 sr-ops：$old → $new"
}

# ---------- rollback ----------
do_rollback() {
  need_root
  detect_installed
  installed || die "没有在 $DIR 找到已安装的 sr-ops"
  local ts="$TO"
  if [ -z "$ts" ]; then
    ts="$(ls -1 "$(backup_root)" 2>/dev/null | sort -r | head -1 || true)"
  fi
  [ -n "$ts" ] || die "没有可用的备份（$(backup_root)）"
  local old; old="$(read_version "$(backup_root)/$ts/VERSION")"
  echo "即将把程序回滚到备份 $ts（版本 $old）；当前版本 $(read_version "$DIR/VERSION")。数据目录不会改动。"
  echo "  注意：如果新版本已经修改过数据格式，旧版本可能无法识别；数据目录的备份在 $(backup_root)/$ts/data.tgz（如有）。"
  confirm "继续回滚？" || die "已取消"
  local port; port="$(listen_port)"; [ -n "$port" ] || port=8766
  sysctl_ stop "$SERVICE" || true
  restore_program "$ts"
  if [ "$SYSTEMD" = 1 ]; then
    systemctl start "$SERVICE" || true
    wait_health "$port" 30 || die "回滚后服务没有正常启动，请查看：journalctl -u $SERVICE -n 80 --no-pager"
  fi
  log "已回滚到 $old"
}

# ---------- uninstall ----------
do_uninstall() {
  need_root
  detect_installed
  if ! installed && [ ! -f "$UNIT" ]; then die "没有找到已安装的 sr-ops（安装目录 $DIR）"; fi
  echo "即将卸载 sr-ops（安装目录 $DIR）："
  echo "  · 停止并禁用服务，删除 systemd unit、程序文件和配置"
  if [ "$PURGE" = 1 ]; then
    echo "  ${c_red}· --purge：同时删除数据目录 $DIR/data（用户、集群连接信息、巡检报告、审计日志、secret.key、上传的安装包）、全部备份"
    echo "    和系统用户 $RUN_USER —— 不可恢复${c_off}"
  else
    echo "  · 数据目录 $DIR/data 与备份保留（重新安装到同一目录即可继续使用；要彻底删除加 --purge）"
  fi
  confirm "确认卸载？" || die "已取消"
  if [ "$PURGE" = 1 ] && [ "$YES" = 0 ]; then
    local a; read -r -p "再次确认：输入安装目录 $DIR 以删除全部数据：" a || true
    [ "$a" = "$DIR" ] || die "输入不一致，已取消"
  fi

  if [ "$SYSTEMD" = 1 ]; then
    log "停止并禁用服务"
    systemctl stop "$SERVICE" 2>/dev/null || true
    systemctl disable "$SERVICE" >/dev/null 2>&1 || true
    rm -f "$UNIT"
    systemctl daemon-reload
    systemctl reset-failed "$SERVICE" 2>/dev/null || true
  fi
  if [ "$PURGE" = 1 ]; then
    log "删除 $DIR（含数据）"
    rm -rf "$DIR"
    if [ "$SYSTEMD" = 1 ] && id "$RUN_USER" >/dev/null 2>&1; then
      userdel "$RUN_USER" 2>/dev/null || warn "系统用户 $RUN_USER 未能删除，可手工执行 userdel $RUN_USER"
    fi
    log "已彻底卸载"
  else
    log "删除程序文件（保留 data/ 与 backup/）"
    rm -rf "$DIR/web" "$DIR/sr-ops" "$DIR/VERSION" "$DIR/README.md" "$DIR/sr-ops.conf"
    # 脚本自身最后删除（正在运行的脚本被 unlink 不影响继续执行）
    rm -f "$DIR/install.sh"
    log "已卸载。数据保留在 $DIR/data"
  fi
}

# ---------- status ----------
do_status() {
  detect_installed
  if ! installed; then
    echo "未安装（安装目录 $DIR）"; exit 3
  fi
  local port; port="$(listen_port)"
  echo "安装目录：$DIR"
  echo "安装版本：$(read_version "$DIR/VERSION")"
  echo "监听地址：$(conf_value SR_LISTEN)"
  if [ "$SYSTEMD" = 1 ] && command -v systemctl >/dev/null 2>&1; then
    echo "服务状态：$(systemctl is-active "$SERVICE" 2>/dev/null || true)（开机自启：$(systemctl is-enabled "$SERVICE" 2>/dev/null || true)）"
  fi
  local v
  if v="$(health "$port")"; then echo "健康检查：正常（运行版本 ${v:-?}）"; else echo "健康检查：无响应（端口 $port）"; fi
  [ -d "$DIR/data" ] && echo "数据目录：$DIR/data（$(du -sh "$DIR/data" 2>/dev/null | awk '{print $1}')）"
  if [ -d "$(backup_root)" ]; then echo "程序备份：$(ls -1 "$(backup_root)" | tr '\n' ' ')"; fi
}

case "$CMD" in
  install) do_install ;;
  upgrade) do_upgrade ;;
  rollback) do_rollback ;;
  uninstall) do_uninstall ;;
  status) do_status ;;
esac
