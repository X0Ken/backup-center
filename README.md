# Backup Center

单机部署的 ZFS 备份与恢复管理平台。Go 管理服务、React 管理界面、SQLite 元数据、NFSv4 和 Linux LIO iSCSI 数据接口。运行不依赖云服务或容器。

> 存储快照保证存储层的一致性，不自动保证在线数据库的应用一致性。MySQL 必须使用匹配版本的 XtraBackup 完成备份与 prepare，PostgreSQL 使用 pg_basebackup 和 pg_verifybackup。不要直接 rsync 在线数据库目录。

## 能力

- 文件仓库与 zvol 块卷、压缩、文件仓库配额、原生 AES-256-GCM 加密及密钥加载/卸载。
- 手动/递归/定时快照，小时/天/周/月/年分层保留，清理预览、显式保护、克隆和复制基线保护。使用 ZFS `createtxg` 区分同一秒内的快照，保护最新恢复点。
- 克隆恢复、快照文件浏览/下载、最新快照原地回滚、空间差异比较 API、完整恢复任务记录。
- NFS IP/CIDR 白名单、同步写入、root_squash、只读导出；iSCSI Target、LUN、显式 initiator ACL、会话查看。
- SSH 全量/增量 ZFS 复制、严格主机指纹验证、断点续传和接收端快照 GUID 校验。
- 池健康、SMART、容量历史、ARC 命中率、IOPS/吞吐量、容量与复制延迟告警、HTTPS Webhook 和 STARTTLS 邮件通知。
- 管理员/操作员角色、bcrypt 密码、HttpOnly/Secure/SameSite 会话、CSRF 校验、登录限速、操作审计。
- 持久化串行任务队列；重启继续排队任务，将执行中的任务明确标记为中断，不盲目重放破坏性操作。
- 配置 JSON 导出、一致性 SQLite 检查点、口令加密的完整灾难恢复包（包含用户、配置、加密密钥、SSH 私钥）。
- 源端 rsync/XtraBackup/pg_basebackup 脚本生成。脚本校验挂载、排他锁、暂存写入、SHA-256 校验，并原子发布 `current` 指向完成的备份。

设计原稿见 [docs/readme.md](docs/readme.md)。本版本的管理边界：网络配置、池创建/扩容及系统组件升级通过受控主机运维完成；LDAP/AD 和 SNMP 未实现，也没有放置空操作入口。平台不会自动格式化未知磁盘，不自动更改业务网络，不将“任务已提交”显示为“备份已成功”。

## 构建与检查

需要 Go **1.26.9+**（CGO 和 C 编译器）、Node.js 22+。依赖版本锁定在 `go.sum` 和 `web/package-lock.json`。

```bash
./scripts/build.sh
# 也可分别运行
GOTOOLCHAIN=go1.26.9 go test -race ./...
go vet ./...
npm ci --prefix web
npm run build --prefix web
```

管理服务必须在 Linux 主机运行，依赖 `zfs`、`zpool`、`nfs-utils`、`targetcli`、`smartctl`、`ssh`、`ssh-keygen`、`ssh-keyscan`。可在满足这些依赖的 Linux 发行版上部署。

安全扫描：

```bash
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
npm audit --prefix web --omit=dev --registry=https://registry.npmjs.org
```

## 安装

OpenCloudOS 9 可先运行 `sudo ./scripts/install-zfs-opencloudos.sh` 安装经 SHA-256 校验的固定版本 OpenZFS、匹配内核开发包与 DKMS 模块。此步骤需要软件源访问能力，不创建或清空存储池。

先准备一个名为 `backup-pool` 的真实 ZFS 池。**安装脚本不擦盘**。首次创建 Mirror 的破坏性脚本独立为 `scripts/provision-mirror.sh`，必须传入明确擦盘参数、两个 NVMe 设备及其预期序列号；该工具拒绝已挂载设备、系统盘和跨盘卷组。仅在已经核实磁盘并获准清空时使用。

```bash
sudo ./scripts/install.sh
sudo cat /etc/backup-center/initial-admin-password
```

安装后访问 `https://192.0.2.10`，管理账号为 `admin`，首次登录强制修改密码。安装器生成设备专属 TLS 证书；企业上线时应将 `/etc/backup-center/tls/server.crt` 和 `server.key` 替换为内部 CA 签发的证书，再执行 `nginx -t && systemctl reload nginx`。证书需覆盖实际访问 IP/DNS。验证设备证书指纹：

```bash
openssl x509 -in /etc/backup-center/tls/server.crt -noout -sha256 -fingerprint
```

平台只管理 `backup-pool/backup-center` 之下带有 `org.backup-center:managed=yes` 标记的数据集。

`backupd` 监听 `127.0.0.1:8080`，Nginx 对外提供 HTTPS。服务端需要管理 ZFS、NFS 和 LIO，因此使用 root systemd 服务；HTTP API 不提供通用 shell 或任意命令执行入口。部署配置保留宿主机挂载传播，否则 ZFS 数据集会只在私有挂载命名空间中可见，NFS 无法访问。

ZFS 模块应通过 DKMS 管理，内核升级后先确认 `dkms status` 有匹配内核的 installed 记录，再重启。月度 scrub 由 `backup-center-scrub.timer` 执行。文件密钥在 `zfs-mount` 之前加载。

## 接入业务备份

1. 创建文件仓库或块卷，设置合理的容量上限。文件仓库默认 `UID:GID=1000:1000`、目录权限 `0770`，API 创建时可指定 `owner_uid` 和 `owner_gid`。
2. 在“访问协议”中仅允许真实源服务器 IP/CIDR 或 initiator IQN，启用导出并确认任务结果。iSCSI 应使用隔离的存储网络；ACL 不替代网络隔离或传输加密。同一个普通文件系统只能有一个块设备写入端。
3. 打开“接入指南”，复制挂载命令和对应的备份工具脚本。NFS 的 root_squash 不会赋予远端 root 写入权限；使用与仓库匹配的专用 UID/GID，并确保该用户有源数据读取权限。需要更改 UID/GID 时由受控主机运维执行 `chown`，不要为了方便取消 root_squash。
4. 在源端配置 cron/systemd timer 执行生成的脚本。源端自行安装匹配业务版本的备份工具和保护凭证文件。脚本只在校验完成后切换 `current`；失败时保留暂存目录以供诊断。脚本不会自动删除历史 runs，必须按业务保留窗口审慎配置清理，避免占满空间。
5. 配置存储快照和分层保留策略。定时快照可能包含暂存文件，但 `current` 永远指向最后一个已完成备份；恢复时选择快照中的 `current` 对应版本。对业务 RPO 要同时评估源端备份间隔与快照间隔。
6. 设置邮件/Webhook，定期真实恢复文件和数据库，记录恢复时间与校验结果；需要异地容灾时配置独立主机上的副本。

NFS 使用内核页缓存与 ZFS 校验，不将响应成功等同于数据库可启动。文件校验和可以验证数据传输与读取，数据库仍须执行真实启动及业务查询演练。

## 恢复

优先克隆指定快照为新仓库，再配置独立导出，避免影响源仓库。文件级恢复通过快照浏览器下载普通文件；路径访问使用 Go `os.Root` 限制在快照内部，阻止 `../` 和符号链接越界。

原地回滚要求输入仓库名、确认已停止写入，并先关闭导出。平台只执行不带递归销毁参数的 `zfs rollback`；存在更新快照时拒绝操作，防止隐式删除后续快照或克隆。更早版本的恢复请使用克隆。

保留策略只清理平台命名的快照；外部快照、最新恢复点、显式 hold、有克隆依赖的恢复点、复制基线均受保护。保留策略不是不可变备份：拥有设备 root 权限的人员仍能删除数据，应保留独立的离线或异地副本。

## 异地复制

1. 在接收端预先创建专用父数据集和专用备份账号，授予接收所需的 ZFS 权限；建议对 SSH 密钥配置 forced-command 和禁止转发。
2. 将界面展示的复制公钥加入该账号。通过独立渠道从目标设备获取 `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` 的 SHA256 指纹，再在界面中添加信任。不会自动接受未知主机。
3. 配置源仓库、主机、SSH 端口和目标数据集，执行首次复制。后续通过共同的快照 GUID 选择增量基线，成功后保留新基线并释放旧基线。
4. 中断时保留接收端 resume token；下次任务使用 `zfs send -t` 续传。接收采用 `-u` 不自动挂载、不使用 `-F` 覆盖不匹配的目标数据。

复制目标应为专用数据集，不应混入接收端自行修改的数据。请监控复制失败和延迟；配置任务不会自动证明异地恢复能力。

## 平台灾难恢复

在“系统管理”下载加密恢复包并独立保存口令，推荐在用户、仓库、策略、通知或密钥变更后重新导出。加密采用 scrypt 派生密钥和 AES-256-GCM，包含仓库原生加密密钥和复制私钥。丢失密钥的加密仓库无法恢复。

更换系统盘或设备后，安装相同/兼容版本 ZFS，导入数据池，再恢复至**全新的**状态目录：

```bash
sudo systemctl stop backupd
# 在 0600 文件中写入口令，不将口令放入命令行或 shell 历史
sudo /opt/backup-center/backupd --data-dir /var/lib/backup-center-restored \
  --pool backup-pool --restore-config /secure/device.bcbackup \
  --password-file /secure/backup-passphrase
```

恢复只写入平台配置与密钥，不会覆盖已有仓库数据，不会自动激活协议导出。正式启用前将恢复结果放到标准目录 `/var/lib/backup-center`，以保持 ZFS 数据集的 keylocation 与密钥文件路径一致；若使用其他目录，须先显式更新相应数据集的 keylocation 和服务配置。然后执行 `zfs load-key -a`、`zfs mount -a`，逐项核对仓库后在界面重新应用导出。原 SSH 登录会话不会恢复。JSON 导出不包含密钥或用户密码，不能单独替代完整恢复包。

## 运维与升级

```bash
systemctl status backupd nginx
journalctl -u backupd -f
zpool status -P backup-pool
zpool list backup-pool
zfs list -r backup-pool/backup-center
dkms status
systemctl list-timers backup-center-scrub.timer
```

升级前导出加密配置备份并确认池健康。运行构建检查，再执行安装脚本；安装器保留上一个二进制 `backupd.previous`。升级可能包含数据库迁移，回滚时必须配套使用对应版本的一致性配置检查点，不能只替换旧二进制。

存储容量取决于实际磁盘与池布局。应预留至少 20% 空间，并按实际数据增长调整配额和保留窗口。单机与单电源/系统盘故障不由磁盘 Mirror 自动消除。

## 验收与开发

只读页面不会生成演示数据。所有仓库、快照、池和协议状态来自真实宿主机。`scripts/acceptance.py` 必须在设备上显式 `--run`，创建小型临时仓库并清理；包含真实文件校验、克隆/回滚、NFS 内核挂载与写入、生成脚本、原生加密、LIO、SSH 全量/增量/中断续传以及加密配置恢复。仅对没有生产写入的受控环境运行该测试，因为复制验收会临时安装限制目标的 forced-command SSH 测试公钥，并在结束时还原。

```bash
sudo python3 scripts/acceptance.py --run \
  --password-file /etc/backup-center/initial-admin-password
# 首次运行测试会完成强制密码修改，并更新该 0600 文件。

# 浏览器验收：需已安装 Playwright Chromium，或指定系统 Chrome
cd web
BC_TEST_PASSWORD_FILE=/secure/admin-password \
BACKUP_CENTER_URL=https://192.0.2.10 \
BC_TEST_MUTATION=1 npm test
```

测试只有在明确设置 `BC_TEST_MUTATION=1` 时，才通过界面创建并删除专属验收仓库。日常修改请同时运行 Go 测试、竞态检测、`go vet` 和前端生产构建；协议、复制和恢复逻辑的变更还应执行真实设备验收。

GitHub Actions 模板见 `deploy/github-actions-check.yml`；具备 workflow 写入权限后，可将其放入 `.github/workflows/check.yml` 启用云端检查。
