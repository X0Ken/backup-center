# Backup Center

单机部署的 ZFS 备份与恢复管理平台。Go 管理服务、React 管理界面、SQLite 元数据、NFSv4 和 Linux LIO iSCSI 数据接口。运行不依赖云服务或容器。

> 存储快照保证存储层的一致性，不自动保证在线数据库的应用一致性。MySQL 必须使用匹配版本的 XtraBackup 完成备份与 prepare，PostgreSQL 使用 pg_basebackup 和 pg_verifybackup。不要直接 rsync 在线数据库目录。

## 能力

- 文件仓库与 zvol 块卷、压缩、文件仓库配额、原生 AES-256-GCM 加密及密钥加载/卸载。
- 手动/递归/定时快照，小时/天/周/月/年分层保留，清理预览、显式保护、克隆和复制基线保护。使用 ZFS `createtxg` 区分同一秒内的快照，保护最新恢复点。
- 克隆恢复、快照文件浏览/下载、最新快照原地回滚、空间差异比较 API、完整恢复任务记录。
- 仓库、快照、策略和共享访问列表支持关键词筛选与分页，表格显示总数、页码、页大小和跳转工具；仓库行提供策略编辑与快照筛选快捷入口。
- 概览显示注册仓库的实际快照总数；iSCSI 目标端 IQN 由平台生成并固定，已有目标保留原 IQN。
- NFS IP/CIDR 白名单、同步写入、root_squash、只读导出；iSCSI Target、LUN、显式 initiator ACL、会话查看。
- 同版本平台间 HTTPS 全量/增量 ZFS 复制、一次性配对与固定证书验证、异步断点续传和接收端快照 GUID 校验。
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
2. 在“共享访问”中仅允许真实源服务器 IP/CIDR 或 initiator IQN，添加访问方式并确认任务结果。iSCSI 应使用隔离的存储网络；ACL 不替代网络隔离或传输加密。同一个普通文件系统只能有一个块设备写入端。
3. 打开“接入指南”，复制挂载命令和对应的备份工具脚本。NFS 的 root_squash 不会赋予远端 root 写入权限；使用与仓库匹配的专用 UID/GID，并确保该用户有源数据读取权限。需要更改 UID/GID 时由受控主机运维执行 `chown`，不要为了方便取消 root_squash。
4. 在源端配置 cron/systemd timer 执行生成的脚本。源端自行安装匹配业务版本的备份工具和保护凭证文件。脚本只在校验完成后切换 `current`；失败时保留暂存目录以供诊断。脚本不会自动删除历史 runs，必须按业务保留窗口审慎配置清理，避免占满空间。
5. 在“快照策略”设置小时、天、周、月、年保留数量，系统按最细非零层级自动调度（小时整点、每日零点、周一零点、每月首日或每年首日），无需配置 Cron。调度和保留均按服务器时区计算；启用或恢复运行时补建当前周期缺失的快照，历史恢复点随时间积累。定时快照可能包含暂存文件，但 `current` 永远指向最后一个已完成备份；恢复时选择快照中的 `current` 对应版本。对业务 RPO 要同时评估源端备份间隔与快照间隔。
6. 设置邮件/Webhook，定期真实恢复文件和数据库，记录恢复时间与校验结果；需要异地容灾时配置独立主机上的副本。

NFS 使用内核页缓存与 ZFS 校验，不将响应成功等同于数据库可启动。文件校验和可以验证数据传输与读取，数据库仍须执行真实启动及业务查询演练。

## 恢复

在“快照”选择“创建仓库”，基于指定快照创建同类型的新仓库；然后在“共享访问”添加访问方式，按接入指南挂载新仓库查看和取回数据。新仓库继承快照内容并独立写入，源快照会因克隆依赖受到保护。文件级恢复通过快照浏览器下载普通文件；路径访问使用 Go `os.Root` 限制在快照内部，阻止 `../` 和符号链接越界。

原地回滚要求输入仓库名、确认已停止写入，并先关闭导出。平台只执行不带递归销毁参数的 `zfs rollback`；存在更新快照时拒绝操作，防止隐式删除后续快照或克隆。更早版本的恢复请使用克隆。

保留策略只清理平台命名的快照；外部快照、最新恢复点、显式 hold、有克隆依赖的恢复点、复制基线均受保护。保留策略不是不可变备份：拥有设备 root 权限的人员仍能删除数据，应保留独立的离线或异地副本。

## 异地复制

1. 目标端部署**完全相同版本**的平台，备份池由目标平台管理。在目标“系统管理 → 异地复制平台互信”生成十分钟有效的一次性接收配对码。
2. 在发送端同一区域填写目标 HTTPS 地址并导入配对码。平台固定配对码携带的 TLS 证书指纹，建立仅有复制接收权限的凭证；不需要手工配置 SSH、公钥、远端账号或数据集。凭证加密保存；证书变更后需重新配对同一平台。
3. “异地复制 → 新建任务”只选源仓库即可。目标名称默认为 `【异地复制】原名称`，可修改并支持中文。提交立即返回，后台幂等登记远端专用仓库并记录仓库 ID，然后复制最新快照。未有快照时先登记仓库并等待。
4. 此后每次快照完成触发后台复制，不再配置独立 Cron。使用独立传输队列，不占用本地存储操作队列。任务按顺序传输，连接中断、进程重启和接收中断后自动退避重试；接收端保留 ZFS resume token，下次使用 `zfs send -t` 续传。等待和传输均持久化，传输中创建的新快照会在后续处理。
5. 两端以快照 GUID 核对增量基线，确认成功后才推进基线。失败传输所需的源快照和已确认基线均受 hold 保护。每次发送最新快照，首次不会复制所有历史快照，后续也不会补齐中间所有恢复点。
6. 接收仓库不挂载、不对外共享、不执行强制覆盖，避免客户端修改影响后续增量。需要恢复时，从远端快照创建新仓库，再共享访问。支持文件仓库、块卷，以及原生加密仓库；平台管理的原始密钥随固定证书的加密连接交给目标端，目标以 0600 保存并纳入加密配置备份。

可以暂停/启用任务、立即复制和删除任务；暂停会中断当前传输并保留续传状态。删除只移除任务并释放本机基线及传输保护，远端仓库和数据保留。目标快照当前不会自动清理；应监控目标容量并定期进行真实恢复演练。撤销目标信任前须删除发送端关联任务；相同目标可用新配对码更新连接，保留原任务和远端 ID。加密配置恢复后平台连接和复制任务默认暂停，重新配对确认后再启用。

升级时旧版 SSH 任务保留记录但停止运行，需要通过平台互信重新创建。管理员配对操作经过会话与 CSRF 验证；平台间令牌仅能访问其绑定的接收仓库，不能用于登录或管理其他仓库。传输接口需使用随安装包提供的 nginx 配置（流式请求、不落代理临时文件、允许大请求及长时间接收）。

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

只读页面不会生成演示数据。所有仓库、快照、池和协议状态来自真实宿主机。`scripts/acceptance.py` 必须在设备上显式 `--run`，创建小型临时仓库并清理；包含真实文件校验、克隆/回滚、NFS 内核挂载与写入、生成脚本、原生加密、LIO 以及加密配置恢复。平台间复制另由 `scripts/verify-platform-replication.py` 在专用隔离实例上显式 `--run` 验收，覆盖远端幂等登记、全量/增量、真实上传断开、两端重启续传、加密仓库和块卷的克隆恢复，保留验收数据。仅在没有生产写入的受控环境运行这些测试。

```bash
sudo python3 scripts/acceptance.py --run \
  --password-file /etc/backup-center/initial-admin-password
# 首次运行测试会完成强制密码修改，并更新该 0600 文件。

# 浏览器验收：需已安装 Playwright Chromium，或指定系统 Chrome
cd web
BC_TEST_USER=admin BC_TEST_PASSWORD_FILE=/secure/admin-password \
BACKUP_CENTER_URL=https://192.0.2.10 \
BC_TEST_MUTATION=1 npm test
```

测试只有在明确设置 `BC_TEST_MUTATION=1` 时，才通过界面创建并删除专属验收仓库。日常修改请同时运行 Go 测试、竞态检测、`go vet` 和前端生产构建；协议、复制和恢复逻辑的变更还应执行真实设备验收。

GitHub Actions 模板见 `deploy/github-actions-check.yml`；具备 workflow 写入权限后，可将其放入 `.github/workflows/check.yml` 启用云端检查。


两台客户端共享访问验收使用 `scripts/verify-shared-access.py --run`：通过 `--url`、`--certificate`、`--username` 和 `--password-file` 指定平台；通过 `--clients`、`--client-addresses`、`--storage-address`、`--ssh-user`、`--ssh-password-file` 和 `--ssh-known-hosts` 指定两台 SSH 客户端、其存储网络 IP、平台存储地址和受保护的登录凭证/主机公钥文件。客户端需安装 `nfs-utils` 和 `iscsi-initiator-utils`、启动 `iscsid`，且 SSH 账号可免交互 sudo。

该验收始终保留唯一命名的四个仓库、受保护快照、白名单访问配置、客户端挂载和校验文件，并将位置与 SHA-256 输出到终端。每个客户端使用独立的 iSCSI 测试卷，只对该测试目标映射出的全新、空白且容量匹配的 512 MiB LUN 创建文件系统。NFS 验收覆盖双端写入和互读、快照克隆挂载及只读限制；iSCSI 验收覆盖文件写入、卸载快照、注销/重新登录及内容校验。客户端挂载保留到卸载或客户端重启，数据和平台访问配置持久保存。


## 数据同步

“数据同步”统一管理源主机到文件仓库的单向采集：SSH 方式由平台执行 rsync（源端也需 rsync），SFTP 方式使用平台上的 rclone，源端只需现有 SFTP 服务，不执行远程 shell 命令。支持密码、OpenSSH/PEM 私钥和私钥口令；凭证经 AES-GCM 加密保存，主密钥随完整加密配置备份保存，首次连接必须确认 SSH 主机指纹。

创建任务时选择业务源目录和空的专用文件仓库。已有快照策略保持不变；没有策略时自动建立小时 24、天 30、周 12、月 12、年 3 的默认策略。同步任务默认每 60 秒检查，不填写 Cron。实际同步延迟取决于目录扫描和传输时间，失败会退避重试，最多两项任务并发。

同步通过隔离工作副本完成，优先使用 ZFS 的 reflink 克隆旧版本，平台不支持 reflink 时 GNU cp 回退到本地复制。成功后执行空目录/删除比例检查、刷新数据并原子切换 `current`。失败、取消和进程重启不会把未完成工作副本发布为当前版本。SFTP 重试会从已发布版本重建独立工作副本，避免把中断下载的临时文件作为有效数据；rsync 可继续利用其暂存文件。实时目录保留当前及上一版本，更早数据由 ZFS 快照保留。快照浏览自动进入有效版本；NFS 挂载同步仓库后从 `current/` 读取。

首次成功自动建立基线快照。后续分层策略只对当前周期内成功发布的新版本创建自动恢复点，持续同步期间等待完成；源端中断时不重复用旧副本冒充新的恢复点。专用仓库禁止执行文件、设备和 setuid，仅允许只读共享。修改或删除数据同步任务不会自动删除仓库数据或快照；来源和仓库绑定后不能更换，迁移来源请新建任务。

SSH/rsync 保留源端可读取的权限、UID/GID、ACL、扩展属性和硬链接；SFTP 只保证普通文件内容和时间等基础信息，不保留 Unix ACL、属主、硬链接或符号链接，平台采用仓库 UID/GID 和受限本地权限保存。文件传输完成不代表在线数据库应用一致性；MySQL/PostgreSQL 等应采集其备份工具生成的完整输出目录。

平台新增依赖 `rsync`、`sshpass`、`rclone`（部署验证版本为官方 1.75.2）及 OpenSSH 客户端。加密配置恢复后，同步任务默认暂停，核对源端和仓库后再启用。密码和私钥不包含在普通 JSON 配置导出中。
