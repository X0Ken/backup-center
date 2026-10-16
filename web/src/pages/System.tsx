import PeerSettings from "./PeerSettings";
import { actionLabel, auditDetail } from "../types";
import React from "react";
import { Button, Message, Modal, Tag } from "@arco-design/web-react";
import { IconPlus } from "@arco-design/web-react/icon";
import { api, bytes, date, useAuth } from "../api";
import { DataTable, ErrorBox, Row, useData } from "../components";
import { Tools } from "../types";
export default function System({
  rev,
  admin,
  form,
  refresh,
  showText,
}: Omit<Tools, "perform" | "repos">) {
  const info = useData("/system", rev);
  const users = useData<Row[]>(admin ? "/users" : "/auth/me", rev);

  function account(u?: Row) {
    form(
      u ? "编辑用户" : "创建用户",
      [
        {
          key: "username",
          label: "用户名",
          value: u?.username,
          required: true,
        },
        {
          key: "password",
          label: u ? "重置密码（留空保持）" : "初始密码",
          type: "password",
          required: !u,
          help: "14 至 72 字节；新用户首次登录必须修改。",
        },
        {
          key: "role",
          label: "角色",
          type: "select",
          value: u?.role || "operator",
          options: [
            { label: "操作员 · 查看、快照与创建快照仓库", value: "operator" },
            { label: "管理员 · 完整管理权限", value: "admin" },
          ],
        },
        {
          key: "disabled",
          label: "禁用账号",
          type: "switch",
          value: u?.disabled || false,
        },
      ],
      async (v) => {
        await api("/users", "POST", { ...v, id: u?.id || 0 });
        Message.success("用户已保存");
        refresh();
      },
    );
  }
  return (
    <>
      <section>
        <div className="section-title">
          <h2>磁盘信息</h2>
          <span className="muted">服务器物理设备与备份池成员状态</span>
        </div>
        <ErrorBox error={info.data?.disk_error} />
        <DataTable
          rows={info.data?.disks || null}
          rowKey="name"
          columns={[
            {
              title: "设备 / 用途",
              render: (_: any, d: Row) => (
                <span>
                  {d.name}
                  <small className="cell-sub">{d.role}</small>
                </span>
              ),
            },
            {
              title: "型号 / 序列号",
              render: (_: any, d: Row) => (
                <span>
                  {d.model || "—"}
                  <small className="cell-sub">{d.serial || "—"}</small>
                </span>
              ),
            },
            { title: "容量", dataIndex: "size", render: bytes },
            {
              title: "介质 / 接口",
              render: (_: any, d: Row) =>
                `${d.media} · ${d.transport || "未报告"}`,
            },
            {
              title: "挂载点",
              render: (_: any, d: Row) => d.mountpoints.join("、") || "—",
            },
            {
              title: "备份池状态",
              render: (_: any, d: Row) =>
                d.pool_state ? (
                  <Tag color={d.pool_state === "ONLINE" ? "green" : "red"}>
                    {(
                      {
                        ONLINE: "在线",
                        DEGRADED: "降级",
                        FAULTED: "故障",
                        OFFLINE: "离线",
                        UNAVAIL: "不可用",
                        REMOVED: "已移除",
                      } as Row
                    )[d.pool_state] || d.pool_state}
                  </Tag>
                ) : (
                  "非池成员"
                ),
            },
          ]}
        />
      </section>
      <section>
        <div className="section-title">
          <h2>设备信息</h2>
          <Tag color="green">本地自治</Tag>
        </div>
        <ErrorBox error={info.error} />
        <div className="info-grid">
          <div>
            <label>主机名</label>
            <strong>{info.data?.hostname || "—"}</strong>
          </div>
          <div>
            <label>平台版本</label>
            <strong>{info.data?.version || "—"}</strong>
          </div>
          <div>
            <label>存储池</label>
            <strong>{info.data?.pool || "—"}</strong>
          </div>
          <div>
            <label>系统时区</label>
            <strong>{info.data?.timezone || "—"}</strong>
          </div>
        </div>
        <pre className="code-block">
          {info.data?.zfs_version || info.data?.zfs_error || "读取中…"}
        </pre>
        <Button
          onClick={() => {
            try {
              showText({
                title: "网络接口信息",
                content: JSON.stringify(JSON.parse(info.data.network), null, 2),
              });
            } catch {
              Message.error("网络信息不可用");
            }
          }}
        >
          查看网络接口
        </Button>
      </section>
      {admin && (
        <>
          <div className="section-toolbar">
            <h2>用户与访问控制</h2>
            <Button
              type="primary"
              icon={<IconPlus />}
              onClick={() => account()}
            >
              创建用户
            </Button>
          </div>
          <DataTable
            rows={users.data}
            columns={[
              { title: "用户名", dataIndex: "username" },
              {
                title: "角色",
                dataIndex: "role",
                render: (v: string) => (v === "admin" ? "管理员" : "操作员"),
              },
              {
                title: "状态",
                render: (_: any, r: Row) => (
                  <Tag color={r.disabled ? "gray" : "green"}>
                    {r.disabled ? "已禁用" : "可用"}
                  </Tag>
                ),
              },
              { title: "创建时间", dataIndex: "created_at", render: date },
              {
                title: "操作",
                render: (_: any, r: Row) => (
                  <Button type="text" onClick={() => account(r)}>
                    编辑
                  </Button>
                ),
              },
            ]}
          />
          <PeerSettings
            rev={rev}
            form={form}
            refresh={refresh}
            showText={showText}
          />
          <section>
            <h2>告警通知与配置备份</h2>
            <div className="settings-actions">
              <Button
                onClick={() =>
                  form(
                    "告警阈值",
                    [
                      {
                        key: "capacity_threshold",
                        label: "容量使用率告警阈值（%）",
                        type: "number",
                        min: 50,
                        max: 99,
                        value: 80,
                      },
                    ],
                    async (v) => {
                      await api("/settings", "PUT", v);
                      Message.success("阈值已更新");
                    },
                  )
                }
              >
                容量阈值
              </Button>
              <Button
                onClick={() =>
                  form(
                    "配置告警通知",
                    [
                      {
                        key: "webhook",
                        label: "HTTPS Webhook 地址",
                        type: "password",
                        help: "支持企业微信/钉钉文本消息格式；完整替换通知配置。",
                      },
                      { key: "smtp_host", label: "SMTP 主机" },
                      {
                        key: "smtp_port",
                        label: "SMTP STARTTLS 端口",
                        type: "number",
                        value: 587,
                        max: 65535,
                      },
                      { key: "smtp_user", label: "SMTP 用户名" },
                      {
                        key: "smtp_password",
                        label: "SMTP 密码",
                        type: "password",
                      },
                      { key: "from", label: "发件地址" },
                      {
                        key: "to",
                        label: "收件地址",
                        help: "多个地址以英文逗号分隔",
                      },
                    ],
                    async (v) => {
                      await api("/settings", "PUT", {
                        capacity_threshold: 80,
                        notifications: v,
                      });
                      Message.success("通知配置已保存");
                    },
                  )
                }
              >
                邮件与 Webhook
              </Button>
              <Button
                onClick={() => window.location.assign("/api/config/export")}
              >
                导出配置 JSON
              </Button>
              <Button
                onClick={() =>
                  form(
                    "下载加密灾难恢复配置",
                    [
                      {
                        key: "passphrase",
                        label: "配置备份加密口令",
                        type: "password",
                        required: true,
                        help: "至少 14 字节。请独立保存口令；遗失后无法解密。",
                      },
                    ],
                    async (v) => {
                      const response = await fetch("/api/config/backup", {
                        method: "POST",
                        headers: {
                          "Content-Type": "application/json",
                          "X-CSRF-Token": useAuth.getState().csrf,
                        },
                        body: JSON.stringify(v),
                      });
                      if (!response.ok) {
                        const e = await response.json();
                        throw new Error(e.error);
                      }
                      const blob = await response.blob();
                      const url = URL.createObjectURL(blob);
                      const link = document.createElement("a");
                      link.href = url;
                      link.download =
                        "backup-center-" +
                        new Date().toISOString().slice(0, 10) +
                        ".bcbackup";
                      link.click();
                      setTimeout(() => URL.revokeObjectURL(url), 1000);
                      Message.success("加密配置备份已下载");
                    },
                  )
                }
              >
                下载加密恢复包
              </Button>
              <Button
                onClick={() =>
                  api("/config/checkpoint", "POST", {})
                    .then((d) =>
                      showText({
                        title: "一致性配置检查点",
                        content: d.path + "\n\n" + d.note,
                      }),
                    )
                    .catch((e) => Message.error(e.message))
                }
              >
                创建配置检查点
              </Button>
            </div>
            <p className="muted">
              JSON 不包含密钥或密码。灾难恢复还需将配置数据库、加密密钥和 SSH
              私钥备份到独立的安全介质。
            </p>
          </section>
          <section>
            <h2>操作审计</h2>
            <DataTable
              rows={null}
              endpoint="/audit"
              revision={rev}
              tableName="操作审计"
              columns={[
                { title: "时间", dataIndex: "created_at", render: date },
                {
                  title: "操作人",
                  dataIndex: "username",
                  render: (v: string, r: Row) =>
                    v ||
                    (r.user_id === 0 ? "系统自动任务" : `用户 #${r.user_id}`),
                },
                {
                  title: "操作",
                  dataIndex: "action",
                  render: (v: string) => (
                    <span title={v}>{actionLabel(v)}</span>
                  ),
                },
                { title: "目标", dataIndex: "target" },
                { title: "来源 IP", dataIndex: "ip" },
                { title: "详情", dataIndex: "detail", render: auditDetail },
              ]}
            />
          </section>
        </>
      )}
    </>
  );
}
